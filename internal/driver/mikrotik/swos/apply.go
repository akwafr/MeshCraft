package swos

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Ecriture de l'appartenance VLAN des ports.
//
// ┌───────────────────────────────────────────────────────────────────────────┐
// │ VERIFIED SUR HARDWARE LE 29 JUILLET 2026, et ce qui suit garde les deux    │
// │ halves : le chemin write EST exerce, l'encadre d'origine reste      │
// │ lisible pour ce qui ne l'est pas.                                          │
// │                                                                            │
// │ CE QUI L'EST : la pose d'appartenance VLAN et de VLAN natif sur le        │
// │ CSS326-24G-2S+ du parc (10.99.0.36). Vingt-quatre ports written, aucun     │
// │ refus, le masque du VLAN 99 passe de 26 membres a 2, device toujours  │
// │ joignable, ecart nul a la relecture. La table /vlan.b et la page /fwd.b   │
// │ acceptent donc bien le corps que ce fichier compose.                      │
// │                                                                            │
// │ CE QUI NE L'EST PAS : le wake du filtrage (`disabled` → `enabled`),     │
// │ aucun port du parc n'etant en `disabled` ; la restauration d'une          │
// │ sauvegarde ; et le mot de passe d'administration (admin.go).              │
// │                                                                            │
// │ L'ENCADRE D'ORIGINE, pour memory :                                        │
// │                                                                            │
// │ Rien de ce fichier n'a been execute contre un device. Le geste est     │
// │ observed a la lettre dans le JavaScript de l'embedded interface d'un        │
// │ CSS326-24G-2S+ en SwOS 2.17, et les corps produits sont verified sur les   │
// │ captures reelles de cet device (voir apply_test.go, qui compare le     │
// │ corps engendre a la page servie par le switch). Mais l'outil disponible    │
// │ pour l'atteindre a travers le tunnel est en LECTURE SEULE, par decision    │
// │ d'architecture du projet : « write sur un device passe par un         │
// │ connecteur, pas par cet outil ».                                           │
// │                                                                            │
// │ La campagne write est a mener after integration. Les commandes        │
// │ exactes sont dans le rapport de livraison.                                 │
// └───────────────────────────────────────────────────────────────────────────┘
//
// TROIS RULES GOUVERNENT CE FICHIER, ET AUCUNE N'EST NEGOCIABLE
//
// 1. LIRE, MODIFIER, REWRITE — jamais un delta.
//
// SwOS n'accepte pas write partielle. Son interface recompose l'objet ENTIER
// de la page a partir de tous ses champs modifiables et le poste tel quel ; il n'y a
// ni « ajouter », ni « retirer », ni « modifier ce champ ». Poster /fwd.b sans son
// tableau `srt` laisserait donc the device libre de remettre a zero les limites de
// storm de ses vingt-six ports en posant un VLAN. Chaque write RELIT donc la
// page, ne touche que ce qu'elle doit, et renvoie le jeu complet — dont la liste est
// celle de la sauvegarde de configuration de the device (cf. pages.go).
//
// 2. UNE TABLE S'WRITES ENTIRE, donc omettre une ligne la SUPPRIME.
//
// La table des VLAN se poste comme une liste complete. C'est le comportement voulu
// quand on retire un VLAN, et une coupure de service quand on l'a oublie. Toutes les
// lignes lues sont donc conservees telles quelles, y compris leurs champs qu'on ne
// comprend pas.
//
// 3. ON NE TOUCHE JAMAIS AU VLAN QUI PORTE L'ADMINISTRATION.
//
// Ce n'est pas une limitation temporaire qu'on levera par commodite. Ce model n'a
// NI SSH, NI TELNET, NI PORT SERIAL : une write qui retire le port d'uplink de son
// VLAN de management rend the device definitivement muet, et le seul recours est
// un deplacement avec un trombone. Et contrairement a l'Instant On, il n'y a same pas
// le filet du « appliquer puis sauver » — sur SwOS tout est persistant
// immediatement, une coupure d'alimentation n'annule rien.
//
// Le seul filet qui existe est une sauvegarde de configuration prise AVANT, et
// ApplyPorts la prend.

// `ApplyResult` n'est plus defini ici : c'est un ALIAS du type du contrat (cf.
// contract.go). Son champ `Backup` existe POUR CETTE PLATEFORME — c'est written noir
// sur blanc dans internal/driver/state.go : « Existe pour une plateforme et une
// raison : SwOS applique et persiste d'un seul geste, une coupure d'alimentation
// n'annule rien, et cette copie est le SEUL retour arriere qui existe. » Ce que
// ApplyPorts en fait n'a donc pas changed, et ne doit pas changer : la copie est prise
// juste avant la FIRST write, et elle ne se reconstitue pas after.

// ApplyPorts pose l'appartenance VLAN des ports sur the device.
//
// L'ordre : relire state, complete la table des VLAN, decider, prendre une
// sauvegarde, write. Completer la table AVANT d'y affecter des ports n'est pas une
// preference — un port ne peut be membre que d'un VLAN declared, et l'ordre inverse
// laisserait la half du plan appliquee. Et la sauvegarde vient juste avant la
// first write, pas avant la decision : quand il n'y a rien a write, il n'y a
// rien a sauvegarder.
//
// LE GARDE-FOU est dans PlanPorts, qui est PURE et donc verifiable sur des captures
// reelles. Sa rule est propre a SwOS et ne se deduit pas de celle de l'Instant On :
// ce qui est protege n'est pas « le VLAN natif est celui du management » mais
// « l'tagging du VLAN de management sur ce port ne change pas ». Lire son
// commentaire — la version naive ne protegeait RIEN sur l'reference device,
// dont la session arrive tagged sur une cage au VLAN natif 1.
//
// DEUX VLAN sont consideres comme de management : celui que le plan du site souhaite,
// et celui que l'DEVICE declares. Ils different tant qu'un device n'a pas been
// migre, et ne proteger que le premier laisserait rewrite celui qui porte la session.
//
// `plan.Force` leve le garde-fou. Jamais enabled par default, ni par une pose
// automatique : c'est un geste explicite de l'exploitant, qui connait sa topologie
// mieux que meshcraft et accepte le risque du deplacement.
//
// LE NOMMAGE DES PORTS N'EST PAS UN PARAMETER. Il vient de la GEOMETRY lue a l'instant
// sur /link.b (cf. [Facade]), donc du same snapshot que la lecture state qui a
// produit the UI. Le passer a chaque appel laisserait la lecture et la pose diverger
// d'une lettre — et sur une plateforme qui NUMEROTE ses ports, « SFP1 » traduit avec la
// mauvaise front panel est une write sur le mauvais port, i.e. un client coupe.
func (a *Admin) ApplyPorts(ctx context.Context, plan driver.PortPlan) (ApplyResult, error) {
	var out ApplyResult
	var desired = driver.ExpandAllTrunks(plan.Desired, plan.SiteVLANs)
	var force = plan.Force

	// 1. On ne differe jamais contre un etat en cache : les quatre pages sont relues
	//    MAINTENANT. Elles le sont en UNE passe, et le same snapshot sert ensuite a
	//    composer les ecritures — sur une plateforme where chaque page costs deux
	//    requests HTTP et where les access sont serialises, la redemander serait la payer
	//    deux fois pour rien.
	var snap, err = a.readAll(ctx)
	if err != nil {
		return out, err
	}
	var ports = snap.ports
	var current State
	current, err = a.stateOf(snap)
	if err != nil {
		return out, err
	}

	var protected = map[int]string{}
	if plan.MgmtVLAN > 0 {
		protected[plan.MgmtVLAN] = "VLAN de management du plan du site"
	}
	if current.MgmtVLAN > 0 {
		protected[current.MgmtVLAN] = "VLAN de management declared par the device"
	}
	// Aucun VLAN de management connu n'est un cas LEGITIME sur SwOS, contrairement a
	// l'Instant On : `avln` a 0 veut dire « administration accepted depuis n'importe
	// quel VLAN », et un device sorti d'usine est dans cet etat. Refuser
	// to write empecherait d'adopter un CSS neuf.
	//
	// La protection se reporte alors sur le LIEN seul, et c'est dit a the operator :
	// tout port actif est epargne, ce qui est plus conservateur que le cas nominal.
	var mgmtUnknown = len(protected) == 0
	if mgmtUnknown {
		a.step(true, "aucun VLAN de management a epargner",
			"the device accepte l'administration depuis n'importe quel VLAN : "+
				"TOUT port qui porte un lien actif sera epargne, faute de savoir lequel "+
				"porte la session")
	}

	// 2. Completer la table des VLAN.
	//
	//    Le plan VLAN DU SITE est applied en entier, pas seulement les VLAN qu'un port
	//    utilise : un device adopte doit connaitre les VLAN de son site, sans
	//    quoi y affecter un port depuis l'interface de the device — ou brancher un
	//    trunk — ne proposerait rien.
	//
	//    LE VLAN DE MANAGEMENT PASSE EN TETE, et il n'est jamais laisse orphelin :
	//    voir wantedVLANs. Quand il manque a la table, la ligne created receives d'office
	//    les ports qui portent un LIEN — sans quoi la create serait le geste qui coupe
	//    access (voir mgmtSeed).
	var wanted = wantedVLANs(desired, plan.SiteVLANs, protected)
	var seed = mgmtSeed(current, protected, snap.vlanRows, snap.facade)
	var newRows, created, buildErr = withVLANs(snap.vlanRows, wanted, ports, seed)
	if buildErr != nil {
		a.step(false, "table des VLAN non modifiable", buildErr.Error())
		return out, buildErr
	}
	var vid int
	for _, vid = range created {
		var members = seed[vid]
		if len(members) == 0 {
			continue
		}
		var note = fmt.Sprintf("VLAN de management %d absent de la table : la ligne created "+
			"receives d'office les %d port(s) qui portent un lien. Create un VLAN de "+
			"management SANS membre le rendrait filtre et sans porte de sortie — sur un "+
			"port en mode « optional », un VLAN non declared passe librement, et le "+
			"declare vide est donc ce qui coupe access. Faute de savoir lequel porte la "+
			"session, tous les ports actifs sont retenus.", vid, len(members))
		out.Skipped = append(out.Skipped, note)
		a.step(true, "ligne de VLAN de management created avec les ports actifs", note)
	}

	// 3. Poser l'appartenance des ports DANS la table, les VLAN natifs et le MODE
	//    VLAN dans /fwd.b. Trois halves du same geste : l'une dit qui est membre,
	//    l'autre quel VLAN sort untagged, la troisieme si le port regarde la table.
	var fwd = snap.fwd
	var dvid, hasDvid = fwd.Nums(FieldDefaultVLAN)
	if !hasDvid || len(dvid) < ports {
		var e = fmt.Errorf("/fwd.b : tableau « %s » absent ou trop court (%d valeurs "+
			"pour %d ports) — write refused, poser un VLAN natif a l'aveugle "+
			"deplacerait le trafic de ports qu'on n'a pas lus", FieldDefaultVLAN,
			len(dvid), ports)
		a.step(false, "VLAN natifs illisibles", e.Error())
		return out, e
	}
	// Le mode VLAN est FACULTATIF a writing, et il faut que ce soit dit ici : son
	// absence n'empeche pas de poser l'appartenance, elle empeche seulement de
	// wake un filtrage off. Refuser la pose entire pour ca serait refuser de
	// configurer un switch parce qu'on ne peut pas tout configurer.
	var modes, hasModes = fwd.Nums(FieldVLANMode)
	if !hasModes || len(modes) < ports {
		modes = nil
	}

	var decided, planErr = PlanPorts(PortsInput{
		Current: current, Table: newRows, Desired: desired, Facade: snap.facade,
		Modes: modes, Protected: protected, Force: force,
	})
	if planErr != nil {
		a.step(false, "appartenance non posable", planErr.Error())
		return out, planErr
	}
	out.Skipped = append(out.Skipped, decided.Skipped...)
	var changes = decided.Changes
	var s string
	for _, s = range decided.Skipped {
		a.step(false, "port epargne par le garde-fou", s)
	}

	// Appliquer le plan a the snapshot, DANS CET ORDRE : d'abord les appartenances
	// gagnees, ensuite celles perdues, enfin les VLAN natifs et le mode.
	//
	// « AJOUTER AVANT DE RETIRER » n'est pas une elegance : la table s'written d'un seul
	// POST, donc l'ordre a l'interieur de cette boucle ne se voit pas sur le fil. Il se
	// voit dans le RESULT — un port qui doit passer du VLAN 50 au VLAN 60 est membre
	// des deux a l'instant du calcul, jamais d'aucun. Sur une plateforme where
	// appartenance et tagging sont la same data, cet « instant d'aucun » est ce
	// qui coupe un client si writing stops au milieu.
	var touched bool
	var ch PortChange
	for _, ch = range changes {
		for _, vid = range ch.Join {
			var row = rowFor(newRows, vid)
			if row == nil || !row.SetBit(FieldVLANMembers, ch.Index, true) {
				var e = fmt.Errorf("/vlan.b : le masque de membres du VLAN %d n'est pas "+
					"modifiable (champ « %s » de forme inattendue)", vid, FieldVLANMembers)
				a.step(false, "appartenance non posable", e.Error())
				return out, e
			}
		}
		for _, vid = range ch.Leave {
			var row = rowFor(newRows, vid)
			if row == nil {
				continue // la ligne a disparu de la table : rien a en retirer
			}
			if !row.SetBit(FieldVLANMembers, ch.Index, false) {
				var e = fmt.Errorf("/vlan.b : le masque de membres du VLAN %d n'est pas "+
					"modifiable (champ « %s » de forme inattendue)", vid, FieldVLANMembers)
				a.step(false, "appartenance non retirable", e.Error())
				return out, e
			}
		}
		if ch.PVID > 0 {
			dvid[ch.Index] = uint32(ch.PVID)
		}
		if ch.RaiseMode && modes != nil {
			modes[ch.Index] = ch.Mode
			a.step(true, "filtrage VLAN reveille sur "+ch.Port,
				fmt.Sprintf("/fwd.b « %s » passe de disabled a enabled : sans quoi ce port "+
					"IGNORE la table des VLAN et tout ce qui est written pour lui reste "+
					"decoratif", FieldVLANMode))
		}
		out.PortsWritten = append(out.PortsWritten, ch.Port)
		touched = true
	}

	if !touched && len(created) == 0 {
		a.step(true, "rien a write",
			"the device porte already l'appartenance VLAN et les VLAN natifs demandes — "+
				"aucune page n'est postee, ce qui est le comportement voulu sur une "+
				"plateforme sans retour arriere")
		return out, nil
	}

	// 4. La sauvegarde, juste AVANT la first write — et seulement s'il y a
	//    quelque chose a write, une lecture de plus ne se prenant pas pour rien.
	//
	//    Sur cette plateforme, c'est le SEUL retour arriere possible : la
	//    configuration est persistante d'office et une coupure d'alimentation
	//    n'annule rien. Son failure n'empeche pas to write — bloquer une pose pour un
	//    filet serait excessif — mais il est consigne, parce que the operator doit
	//    savoir qu'il travaille sans.
	out.Backup, err = a.Backup(ctx)
	if err != nil {
		a.step(false, "AUCUNE sauvegarde disponible avant write",
			"la pose continue, mais sans retour arriere possible : "+err.Error())
	}

	// 5. Ecrire. La table d'abord : affecter un port a un VLAN inexistant est refused,
	//    et l'ordre inverse laisserait /fwd.b designer un VLAN natif que la table ne
	//    connait pas.
	if err = a.postRaw(ctx, PageVLAN, EncodeTable(newRows),
		fmt.Sprintf("table des VLAN written (%d ligne(s), %s)", len(newRows),
			createdSummary(created))); err != nil {
		return out, err
	}
	out.VLANsCreated = created

	// Puis les VLAN natifs ET le mode VLAN, dans le jeu de champs COMPLET de /fwd.b.
	//
	// AFTER la table, et l'ordre est ici sur le FIL, donc reel : « declare avant de
	// filtrer ». Passer un port de `disabled` a `enabled` active le filtrage a la
	// sortie ; le faire avant que la table ne porte l'appartenance ferait tomber le
	// trafic de ce port pendant l'intervalle entre les deux requests — et
	// definitivement si la seconde fails.
	fwd.SetNums(FieldDefaultVLAN, dvid)
	if modes != nil {
		fwd.SetNums(FieldVLANMode, modes)
	}
	var body *Page
	var missing []string
	body, missing = fwd.Only(FwdWritableFields(ports))
	if len(missing) > 0 {
		// Refuser rather que d'envoyer une page truncated. The device serait libre de
		// remettre a zero l'isolation de ports, les limites de storm et les rates
		// ingress de tous ses ports, et il le ferait en silence.
		var e = fmt.Errorf("/fwd.b : ce firmware ne sert pas les champs %s attendus par "+
			"le connecteur — write refused, renvoyer la page sans eux laisserait "+
			"the device les remettre a zero", strings.Join(missing, ", "))
		a.step(false, "jeu de champs de /fwd.b inattendu", e.Error())
		return out, e
	}
	if err = a.postRaw(ctx, PageFwd, body.Encode(),
		fmt.Sprintf("VLAN natifs written (%d port(s) : %s)", len(out.PortsWritten),
			strings.Join(out.PortsWritten, ", "))); err != nil {
		return out, err
	}

	// 6. Pas d'etape de sauvegarde : sur SwOS, ce qui est poste est already persistant
	//    (cf. Capabilities.Commit). C'est l'exact inverse de l'Instant On, et c'est
	//    pourquoi la copie prise a l'etape 4 est le seul filet.
	a.step(true, "write terminee",
		"SwOS persiste immediatement : il n'y a pas de sauvegarde a faire, et pas de "+
			"reboot qui annulerait ceci")
	return out, nil
}

// PortChange describes ce qu'une pose changerait sur UN port. Rien de plus, et surtout
// rien qui ne change pas.
type PortChange struct {
	Port string `json:"port"`
	// Index est le rang SwOS du port, base 0.
	Index int `json:"index"`
	// Join : les VLAN dont le port doit DEVENIR membre. Ceux dont il l'est already n'y
	// figurent pas.
	Join []int `json:"join,omitempty"`
	// Leave : les VLAN dont le port doit SORTIR, parce que le plan ne les demande
	// plus.
	//
	// Jamais un VLAN de management, jamais le VLAN natif effectif, jamais rien sur un
	// port a plancher (« all »), et rien du tout sur un port actif quand the device
	// accepte l'administration depuis n'importe quel VLAN — [planLeaves] tient ces
	// quatre refus et les explique.
	Leave []int `json:"leave,omitempty"`
	// PVID : le VLAN natif a poser. Zero veut dire « inchange », jamais « VLAN 0 » —
	// SwOS n'a pas de VLAN 0, et le confondre deplacerait le trafic d'un port.
	PVID int `json:"pvid,omitempty"`
	// Mode est la valeur a write dans /fwd.b `vlan`, et RaiseMode dit s'il y a
	// quelque chose a write.
	//
	// DEUX CHAMPS ET NON UN, parce que VLANModeDisabled VAUT ZERO : un `Mode` seul,
	// avec zero pour « inchange », ferait ETEINDRE le filtrage VLAN de tous les ports
	// qu'on ne voulait pas toucher — l'exact contraire de ce que ce champ existe pour
	// faire. Le same trap que `PVID` juste au-dessus, sauf qu'ici la valeur
	// dangereuse est justement celle du zero.
	Mode      uint32 `json:"mode,omitempty"`
	RaiseMode bool   `json:"raise_mode,omitempty"`
}

// PortsInput rassemble ce dont [PlanPorts] a besoin pour decider.
//
// UN AGREGAT ET NON HUIT PARAMETRES, pour la same raison que [driver.PortPlan] en
// porte cinq : le jour where la decision a besoin d'une contrainte de plus, la signature
// change en un seul endroit. Un parameter positionnel ajoute au milieu d'une liste de
// huit est, de surcroit, exactement la faute qui fait passer `ports` there where le code
// attend `prefix` — et sur une plateforme qui NUMEROTE ses ports, une confusion de ce
// genre est une write sur le mauvais port.
type PortsInput struct {
	// Current est state LU, qui donne les VLAN natifs et state des liens.
	Current State
	// Table est la table des VLAN ALREADY COMPLETEE des VLAN manquants : un port ne peut
	// rejoindre qu'un VLAN qui existe.
	Table []*Page
	// Desired est l'intention, par nom de port du model standard.
	Desired map[string]PortState
	// Facade describes la front panel ET son nommage : combien de ports, where sont les cages, et
	// les deux familles de noms qu'un plan peut employer (cf. Facade). Elle vient de la
	// GEOMETRY LUE sur the device, jamais de l'appel (cf. ApplyPorts).
	Facade Facade
	// Modes est le tableau /fwd.b `vlan` tel que the device l'a servi, ou nil quand
	// ce firmware ne le sert pas. Nil fait renoncer a wake un filtrage off —
	// pas a poser l'appartenance.
	Modes []uint32
	// Protected porte les VLAN de management a epargner, et le motif de chacun.
	Protected map[int]string
	// Force leve les garde-fous. Geste explicite de l'exploitant, jamais un default.
	Force bool
}

// PortsPlan est ce qu'une pose ferait, et ce qu'elle refuse de faire.
type PortsPlan struct {
	Changes []PortChange
	// Skipped porte les refus, en phrases destinees a l'exploitant : un garde-fou qui
	// protege sans le dire passe pour un bogue.
	Skipped []string
}

// PlanPorts decide, SANS RIEN WRITE, ce qu'une pose changerait et ce qu'elle
// refusera de toucher.
//
// Fonction PURE, et c'est tout l'interet : c'est elle qui decide quel port garde son
// VLAN natif, donc elle qui decide si the device restera joignable. Une erreur ici
// ne se voit qu'au moment where un switch devient muet, sur un model qui n'a ni SSH, ni
// Telnet, ni serial port. La sortir du chemin network est ce qui permet de l'eprouver
// sur les captures reelles de the device (voir apply_test.go) rather que sur du
// hardware en service.
//
// `in.Table` est la table des VLAN ALREADY COMPLETEE des VLAN manquants : un port ne
// peut rejoindre qu'un VLAN qui existe.
//
// Ce qui est rendu :
//
//   - les changements, dans un ordre stable, et UNIQUEMENT ceux qui bougent quelque
//     chose. Un port already conforme n'y figure pas — sur cette plateforme writing
//     est persistante d'office et sans retour arriere, donc un POST qui ne change
//     rien est un risque pris pour rien, et il serait pris a CHAQUE passage de
//     reconciliation, sur tout le parc ;
//   - les motifs de refus, en clair, destines a l'exploitant.
//
// QUATRE DECISIONS PAR PORT, et non plus deux. La first livraison ne calculait que
// l'appartenance a GAGNER et le VLAN natif ; s'y ajoutent maintenant l'appartenance a
// PERDRE ([planLeaves]) et le wake du filtrage ([planModeRaise]). Les deux
// manquantes were celles qui decidaient si la pose CONVERGE et si elle AGIT.
func PlanPorts(in PortsInput) (PortsPlan, error) {
	var out PortsPlan
	// Deduit rather que passe : deux parameters qui doivent rester consistent sont deux
	// parameters dont l'un peut mentir, et celui-there decide si un port actif est epargne.
	var mgmtUnknown = len(in.Protected) == 0

	var name string
	for _, name = range sortedNames(in.Desired) {
		var want = in.Desired[name]
		if want.Mode == "" || want.Mode == "libre" {
			continue // « absent = non managed » : meshcraft ne reclame pas ce port
		}
		var index, ok = in.Facade.Index(name)
		if !ok {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s ignored : ce nom ne designe pas un port de cet device, qui en a %d et "+
					"les nomme « %s » … « %s » (l'ancienne numerotation « %s1 » reste accepted). "+
					"Envoyer un number de port faux est une write au mauvais endroit.",
				name, in.Facade.Ports(), in.Facade.Name(0),
				in.Facade.Name(in.Facade.Ports()-1), in.Facade.legacy))
			continue
		}

		// LE NOM CANONIQUE, i.e. celui de la FACADE, donc celui de the device.
		//
		// Un plan peut designer un port sous l'ancienne numerotation continue (« GE25 »)
		// et [Facade.Index] l'accepte ; state LU, lui, est toujours named par la front panel
		// (« SFP1 »). Chercher state avec le nom du plan rendrait donc le port INCONNU —
		// PVID 0, aucun VLAN, AUCUN LIEN — et « aucun lien » est tres exactement ce qui
		// LEVE les garde-fous de ce fichier. Une intention heritee de l'ancienne convention
		// aurait ainsi desarme la protection du port qui porte la session, sur la seule
		// plateforme du parc qui n'a ni SSH, ni Telnet, ni serial port.
		//
		// C'est aussi lui qui part dans les messages et dans `PortsWritten` : ce que
		// l'exploitant doit lire est le nom que son switch affiche.
		var canon = in.Facade.Name(index)
		var now = in.Current.Ports[canon]
		var linked = in.Current.LinkUp[canon]
		// Le VLAN natif EFFECTIF, i.e. celui que le port aura a la fin : celui du
		// plan quand il en designe un, celui de the device sinon. C'est lui qui sert de
		// reference a tout le reste — appartenance a garder, mode a wake — parce que
		// la pose doit raisonner sur state d'ARRIVEE, pas sur celui de depart.
		var effectivePVID = now.PVID
		var nativeFrozen bool
		if !in.Force && linked && want.PVID != now.PVID {
			var refus = whyNativeChangeIsUnsafe(canon, now.PVID, want.PVID,
				in.Protected, mgmtUnknown)
			if refus != "" {
				out.Skipped = append(out.Skipped, refus)
				// LE PORT EST GELE EN ENTIER, et pas seulement son VLAN natif.
				//
				// Une version qui refusait le seul changement de natif et continuait sur
				// l'appartenance produisait exactement l'incoherence que la lecture state
				// signale comme une anomalie : un port dont le natif reste 99 et dont
				// l'appartenance est rewritten pour le VLAN 100. Le trafic untagged entre
				// alors en 99 sur un port qui n'est plus membre de 99, et rien n'en sort.
				// Sur le port qui porte la session, c'est la panne exacte que le garde-fou
				// existe pour empecher — il l'aurait causee lui-same.
				nativeFrozen = true
			}
		}
		if nativeFrozen {
			continue
		}
		if want.PVID > 0 {
			effectivePVID = want.PVID
		}

		// L'appartenance GAGNEE : le port doit be membre de son VLAN natif ET de chacun
		// de ses VLAN tagged.
		var ch = PortChange{Port: canon, Index: index}
		var members = map[int]bool{}
		var vid int
		for _, vid = range askedVLANs(want) {
			// Le VLAN 1 n'est PAS dans la table, et il n'a pas a y be : voir
			// implicitVLAN.
			if vid == implicitVLAN {
				members[vid] = true
				continue
			}
			var row = rowFor(in.Table, vid)
			if row == nil {
				// La table a been completee en amont : n'arriver ici voudrait dire qu'un
				// VLAN reclame par un port n'a pas pu y entrer.
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"%s : le VLAN %d n'est pas dans la table — appartenance non applied", canon, vid))
				continue
			}
			var deja, lisible = row.Bit(FieldVLANMembers, index)
			if !lisible {
				return PortsPlan{}, fmt.Errorf("/vlan.b : le masque de membres du VLAN %d "+
					"n'est pas lisible au rang du port %s (champ « %s » absent ou de forme "+
					"inattendue) — l'appartenance serait devinee", vid, canon, FieldVLANMembers)
			}
			members[vid] = true
			if !deja {
				ch.Join = append(ch.Join, vid)
			}
		}

		// L'appartenance PERDUE, avec ses quatre refus.
		var leaves, leaveErr = planLeaves(canon, index, want, effectivePVID, in.Table,
			linked, in.Protected, in.Force)
		if leaveErr != nil {
			return PortsPlan{}, leaveErr
		}
		ch.Leave = leaves.Leave
		out.Skipped = append(out.Skipped, leaves.Refused...)

		if want.PVID > 0 && now.PVID != want.PVID {
			ch.PVID = want.PVID
		}

		// Le REVEIL du filtrage, decide sur l'appartenance d'ARRIVEE.
		if in.Modes != nil && index < len(in.Modes) {
			// `members` ne porte que ce que le plan demande ; l'appartenance d'arrivee
			// comprend aussi ce que le port garde. Sans cette fusion, un port dont le plan
			// ne redit pas le VLAN de management passerait pour non membre, et le wake
			// serait refused sur tous les ports actifs d'un parc correctement configure.
			var held, heldErr = heldVLANs(in.Table, index)
			if heldErr != nil {
				return PortsPlan{}, heldErr
			}
			var leaving = map[int]bool{}
			for _, vid = range ch.Leave {
				leaving[vid] = true
			}
			for _, vid = range held {
				if !leaving[vid] {
					members[vid] = true
				}
			}
			var raise = planModeRaise(canon, in.Modes[index], members, effectivePVID,
				linked, in.Protected, in.Force)
			if raise.Refused != "" {
				out.Skipped = append(out.Skipped, raise.Refused)
			}
			ch.Mode, ch.RaiseMode = raise.Mode, raise.Change
		}

		if len(ch.Join) == 0 && len(ch.Leave) == 0 && ch.PVID == 0 && !ch.RaiseMode {
			continue // already conforme : ne rien envoyer vaut mieux qu'un aller-retour
		}
		out.Changes = append(out.Changes, ch)
	}
	return out, nil
}

// implicitVLAN est le VLAN 1, et il n'est PAS une ligne de la table des VLAN.
//
// OBSERVED sur l'reference device, et c'est une discovery qui change le
// garde-fou : ses deux cages SFP ont un « Default VLAN ID » de 1 (/fwd.b
// `dvid[24]=dvid[25]=0x01`) alors que sa table des VLAN ne contient QUE les VLAN 99
// et 100. Le VLAN 1 existe donc comme default implicite du hardware, sans ligne.
//
// Deux consequences, et les deux comptent :
//
//   - un port dont le VLAN natif est 1 n'a AUCUNE appartenance a poser. Chercher une
//     ligne pour le VLAN 1 et se plaindre de son absence ferait echouer la pose sur un
//     device parfaitement configure — c'est ce que faisait la first version ;
//   - il ne faut SURTOUT PAS create cette ligne. La create transformerait le VLAN 1 en
//     VLAN declared, dont seuls les ports listes seraient membres — i.e.
//     changerait le comportement de tout le trafic untagged de the device, alors
//     que personne ne l'a requested. Le connecteur Instant On saute lui aussi les VLAN
//     inferieurs ou egaux a 1, pour une raison voisine.
const implicitVLAN = 1

// whyNativeChangeIsUnsafe dit pourquoi changer le VLAN natif d'un port ACTIF serait
// dangereux — et rend une string vide quand ce ne l'est pas.
//
// LE GARDE-FOU DE SwOS N'EST PAS CELUI DE L'INSTANT ON, et c'est le observed sur
// hardware qui l'a montre.
//
// Sur l'Instant On, la rule est « ne pas toucher au VLAN natif d'un port actif qui
// est membre untagged du VLAN de management ». Transposee telle quelle ici, elle ne
// protege RIEN : sur l'reference device, la session d'administration arrive
// TAGGED en VLAN 99 sur une cage dont le VLAN natif est 1. Le VLAN natif de ce port
// n'est donc pas le VLAN de management, et la rule de l'Instant On l'aurait laisse
// rewrite librement.
//
// Ce qui compte actually sur SwOS est la RULE D'ETIQUETTAGE : une trame sort d'un
// port non tagged si son VLAN est le VLAN natif de ce port, tagged sinon. Changer le
// VLAN natif d'un port change donc l'tagging de DEUX VLAN — l'ancien natif devient
// tagged, le nouveau devient untagged — et laisse tous les autres inchanges.
//
// La condition de surete est donc precise : le changement doit preserver
// l'tagging de CHAQUE VLAN de management sur ce port. Autrement dit, pour chaque
// VLAN protege v, « v est untagged sur ce port » doit valoir la same chose avant et
// after.
//
// Ce que cela donne en pratique, et c'est exactement le comportement voulu :
//
//   - faire passer une cage d'uplink du VLAN natif 1 au VLAN natif 100, alors que
//     l'administration arrive tagged en 99 : AUTORISE. Le 99 reste tagged avant comme
//     after, la session ne bouge pas ;
//   - faire passer cette same cage au VLAN natif 99 : REFUSED. L'administration
//     passerait de tagged a non tagged, et le voisin qui l'envoie tagged ne serait
//     plus entendu ;
//   - faire passer un access port qui porte l'administration NON tagged vers un
//     autre VLAN : REFUSED. C'est le cas classique, celui que couvrait already la rule de
//     l'Instant On.
//
// Et quand aucun VLAN de management n'est connu, rien n'est prouvable : tout port
// actif est epargne.
func whyNativeChangeIsUnsafe(port string, nowPVID, wantPVID int,
	protected map[int]string, mgmtUnknown bool) string {
	if mgmtUnknown {
		return fmt.Sprintf(
			"%s non modifie : il porte un lien ACTIF et the device accepte "+
				"l'administration depuis n'importe quel VLAN, donc rien ne dit que ce n'est "+
				"pas lui qui porte la session. Ce model n'a aucune console de secours.", port)
	}
	// Ordre stable : deux executions doivent produire le same message.
	var vlans = make([]int, 0, len(protected))
	var v int
	for v = range protected {
		vlans = append(vlans, v)
	}
	sort.Ints(vlans)

	for _, v = range vlans {
		var nonTagueAvant = nowPVID == v
		var nonTagueApres = wantPVID == v
		if nonTagueAvant == nonTagueApres {
			continue // l'tagging de ce VLAN de management ne change pas
		}
		if nonTagueAvant {
			return fmt.Sprintf(
				"%s non modifie : il porte un lien ACTIF et l'administration y arrive NON "+
					"TAGGED dans le VLAN %d (%s). Lui donner le VLAN natif %d ferait passer "+
					"l'administration en tagged sur ce port, et couperait access. Ce model "+
					"n'a ni SSH, ni Telnet, ni serial port : l'erreur se repare sur site.",
				port, v, protected[v], wantPVID)
		}
		return fmt.Sprintf(
			"%s non modifie : il porte un lien ACTIF et l'administration y arrive TAGGED "+
				"dans le VLAN %d (%s). Lui donner le VLAN natif %d ferait sortir "+
				"l'administration sans tag sur ce port, et couperait access. Ce model "+
				"n'a ni SSH, ni Telnet, ni serial port : l'erreur se repare sur site.",
			port, v, protected[v], wantPVID)
	}
	return ""
}

// removalPolicy — ce que la pose retire, et ce qu'elle refuse de retirer.
//
// ELLE RETIRE, MAINTENANT, et il faut dire pourquoi ca a changed. La first
// livraison ne retirait rien : sur SwOS, appartenance et tagging sont la SAME
// data — retirer un port de la liste des membres d'un VLAN, c'est couper ce VLAN sur
// ce port, sans demi-mesure — le VLAN de management vit dans la same table que les
// autres, et il n'existe aucun filet, writing etant persistante d'office. Ne rien
// retirer was donc le choix prudent, et il was DIT a the UI.
//
// Ce qu'il coutait, en revanche, ne se voyait pas : un device pouvait be annonce
// « conforme » en transportant depuis des mois un VLAN que le plan avait retire. Une
// segmentation qui fuit ne se remarque pas ; quand elle se remarque, c'est en
// cherchant autre chose. « Ajout seulement » n'est pas une prudence neutre — c'est un
// parc qui derive dans le sens le plus couteux.
//
// Le retrait est donc fait, avec QUATRE refus qui couvrent chacun une way documentee
// de se couper la main (voir [planLeaves]) : jamais un VLAN de management, jamais le
// VLAN natif effectif, jamais rien sur un port a plancher (« all »), et rien du tout
// sur un port actif quand the device accepte l'administration depuis n'importe quel
// VLAN. Chaque refus est rendu a l'exploitant avec son motif — c'est ce qui remplace
// l'ancienne phrase general, et c'est plus utile : elle disait « la pose ne retire
// rien », les nouvelles disent « ce retrait-there n'a pas been fait, et voici pourquoi ».
func removalPolicy() string {
	return "retrait calcule, sauf sur les ports en « all » et sur les VLAN de management " +
		"des ports qui portent un LIEN — un port sans lien perd bien son appartenance au " +
		"VLAN d'administration, faute de quoi un access port ne pourrait jamais en " +
		"devenir un : chaque retrait refused est rendu avec son motif"
}

// wantedVLANs rassemble les VLAN qu'il faut voir dans la table : ceux du plan du
// site, ceux qu'un port reclame, et ceux qui portent l'ADMINISTRATION.
//
// LES VLAN DE MANAGEMENT EN TETE, et ce n'est pas une preference d'ordre. Un VLAN de
// management absent de la table est un VLAN de management ORPHELIN : le plan du site
// est cense le declare, mais rien ne le garantit — un site importe d'un fichier
// fabrique, un device dont `avln` designe un VLAN que personne n'a modelise, et
// l'appartenance du port d'uplink n'a plus aucune ligne where s'write. La pose posait
// alors tout le reste et laissait precisely le VLAN par lequel elle est arrivee hors
// de la table.
//
// Ils sont ajoutes SANS NOM : `protected` porte un motif destine a l'exploitant
// (« VLAN de management du plan du site »), pas un label d'device, et l'write
// dans le champ `nm` de /vlan.b — 16 octets — donnerait un nom que personne n'a
// choisi. Quand le plan du site nomme ce same VLAN, c'est son nom qui gagne, parce
// qu'il est ajoute avant.
func wantedVLANs(desired map[string]PortState, siteVLANs []VLAN,
	protected map[int]string) []VLAN {
	var byID = map[int]string{}
	var order []int
	var add = func(id int, name string) {
		if id <= 0 {
			return
		}
		var _, seen = byID[id]
		if !seen {
			order = append(order, id)
		}
		// Un nom already connu n'est pas ecrase par un vide : le plan du site nomme,
		// l'intention de port ne nomme pas.
		if byID[id] == "" {
			byID[id] = name
		}
	}
	var v VLAN
	for _, v = range siteVLANs {
		add(v.ID, v.Name)
	}
	var id int
	for _, id = range sortedKeysInt(protected) {
		add(id, "")
	}
	var name string
	for _, name = range sortedNames(desired) {
		var p = desired[name]
		if p.Mode == "" || p.Mode == "libre" {
			continue
		}
		add(p.PVID, "")
		for _, id = range p.Tagged {
			add(id, "")
		}
	}
	sort.Ints(order)
	var out = make([]VLAN, 0, len(order))
	for _, id = range order {
		out = append(out, VLAN{ID: id, Name: byID[id]})
	}
	return out
}

// mgmtSeed dit, pour chaque VLAN de management ABSENT de la table, quels ports la
// ligne a create doit compter parmi ses membres.
//
// # LE TRAP QUE CETTE FONCTION EXISTE POUR DESAMORCER
//
// Create une ligne de VLAN sur SwOS n'est pas un geste neutre : c'est ce qui fait
// PASSER ce VLAN du regime « inconnu de la table » au regime « declared, donc filtre ».
// Sur un port en mode `optional` — le default d'usine, et state des 26 ports de
// l'reference device — un VLAN que la table ne declares pas circule librement ;
// le declare avec un masque de membres VIDE le fait tomber partout, instantanement.
//
// Autrement dit : la way la plus rapide de rendre un CSS muet est de create
// proprement la ligne de son VLAN de management. C'est contre-intuitif, ca ressemble a
// une mise en ordre, et c'est une coupure.
//
// # CE QU'ON RETIENT, ET POURQUOI C'EST LARGE
//
// Tous les ports qui portent un LIEN. Faute de savoir lequel porte la session — SwOS
// ne le dit pas, et `avln` ne nomme qu'un VLAN, pas un port — la seule alternative
// serait de n'en retenir aucun, i.e. de choisir la coupure. Un port actif de
// plus dans un VLAN de management est un exces de portee que l'exploitant peut
// corriger ; un port actif de moins est un deplacement sur site.
//
// Les VLAN de management ALREADY present dans la table ne sont pas concernes : leur
// appartenance est celle que l'exploitant a applied, et [planLeaves] refuse d'y toucher.
//
// Fonction PURE, verifiable sur les captures.
func mgmtSeed(current State, protected map[int]string, rows []*Page,
	f Facade) map[int][]int {
	var out = map[int][]int{}
	var vid int
	for _, vid = range sortedKeysInt(protected) {
		if vid == implicitVLAN || rowFor(rows, vid) != nil {
			continue
		}
		var indices []int
		var i int
		for i = 0; i < f.Ports(); i++ {
			if current.LinkUp[f.Name(i)] {
				indices = append(indices, i)
			}
		}
		if len(indices) > 0 {
			out[vid] = indices
		}
	}
	return out
}

// withVLANs complete la table lue des VLAN manquants, et rend la liste A WRITE.
//
// Les lignes existantes sont conservees TELLES QUELLES, champ pour champ. C'est la
// rule : cette table s'written entire, et on n'y touche pas a ce qu'on ne comprend
// pas — l'isolation par VLAN (`piso`), l'apprentissage (`lrn`), le miroir (`mrr`) et
// le snooping IGMP (`igmp`) sont regles par l'exploitant et ne sont pas a nous.
//
// Les lignes CREEES reprennent les valeurs par default de l'interface de
// the device, observed dans ses definitions de champs, et NON des zeros : `piso`
// et `lrn` valent 1 par default (`u:1`), et un VLAN created avec apprentissage disabled
// inonderait tout son trafic.
//
// `seed` associe a un VLAN les index de ports dont la ligne CREEE doit d'emblee
// compter les membres. Il ne sert qu'aux VLAN de management (cf. [mgmtSeed]), pour la
// raison la moins intuitive de ce fichier : create proprement la ligne d'un VLAN de
// management avec un masque vide est ce qui coupe access.
func withVLANs(rows []*Page, wanted []VLAN, ports int,
	seed map[int][]int) ([]*Page, []int, error) {
	var out = append([]*Page{}, rows...)
	var created []int

	var w VLAN
	for _, w = range wanted {
		if w.ID < 1 || w.ID > 4094 {
			return nil, nil, fmt.Errorf("VLAN %d hors des bornes de la table de SwOS "+
				"(1 a 4094)", w.ID)
		}
		// Le VLAN 1 n'entre JAMAIS dans la table : le create changerait le comportement
		// de tout le trafic untagged de the device (cf. implicitVLAN).
		if w.ID == implicitVLAN {
			continue
		}
		if rowFor(out, w.ID) != nil {
			continue
		}
		if len(out) >= 250 {
			return nil, nil, fmt.Errorf("la table des VLAN de SwOS est limitee a 250 "+
				"entries et en porte already %d : le VLAN %d ne peut pas be ajoute",
				len(out), w.ID)
		}
		var row = newVLANRow(w, ports)
		var index int
		for _, index = range seed[w.ID] {
			if !row.SetBit(FieldVLANMembers, index, true) {
				return nil, nil, fmt.Errorf("VLAN %d : le masque de membres de la ligne a "+
					"create n'accepte pas le port de rang %d — la ligne serait created SANS "+
					"membre, ce qui sur un VLAN de management coupe access", w.ID, index)
			}
		}
		out = append(out, row)
		created = append(created, w.ID)
	}
	// Ordre stable : deux executions doivent produire exactement le same corps,
	// sinon comparer deux transcripts ne veut plus rien dire.
	sort.SliceStable(out, func(i, j int) bool {
		var a, aOK = out[i].Num(FieldVLANID)
		var b, bOK = out[j].Num(FieldVLANID)
		if !aOK || !bOK {
			return false
		}
		return a < b
	})
	sort.Ints(created)
	return out, created, nil
}

// newVLANRow construit une ligne de table pour un VLAN a create.
//
// L'ordre des champs et les valeurs par default reproduisent ce que l'interface de
// the device engendre. Le masque de membres part VIDE : c'est ApplyPorts qui y
// pose les ports, et un VLAN created avec tous les ports membres — ce que fait le
// default de l'interface (`u:268435455`) — ferait passer tout le trafic partout.
func newVLANRow(v VLAN, ports int) *Page {
	var row = NewPage()
	row.SetText(FieldVLANName, v.Name)
	// Le masque prend la FORME que the device emploie pour ce nombre de ports :
	// scalaire jusqu'a 32, tableau de mots au-dela. Sans quoi SetBit refuserait, et
	// l'appartenance ne serait pas posable.
	if ports <= 32 {
		row.SetNum(FieldVLANMembers, 0)
	} else {
		var words = make([]uint32, (ports+31)/32)
		row.SetNums(FieldVLANMembers, words)
	}
	row.SetNum(FieldVLANID, uint32(v.ID))
	// Defauts de l'interface de the device : isolation de ports et apprentissage
	// actifs, miroir et snooping IGMP inactifs.
	row.SetNum("piso", 1)
	row.SetNum("lrn", 1)
	row.SetNum("mrr", 0)
	row.SetNum("igmp", 0)
	return row
}

// rowFor retrouve la ligne d'un VLAN dans la table.
func rowFor(rows []*Page, vid int) *Page {
	var r *Page
	for _, r = range rows {
		var v, ok = r.Num(FieldVLANID)
		if ok && int(v) == vid {
			return r
		}
	}
	return nil
}

func sortedNames(m map[string]PortState) []string {
	var out = make([]string, 0, len(m))
	var k string
	for k = range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func createdSummary(created []int) string {
	if len(created) == 0 {
		return "aucun VLAN created"
	}
	var parts = make([]string, 0, len(created))
	var v int
	for _, v = range created {
		parts = append(parts, strconv.Itoa(v))
	}
	return "VLAN created(s) : " + joinComma(parts)
}

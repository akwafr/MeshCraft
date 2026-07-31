package driver

// Les types que la couche API echange avec un connecteur : state lu, l'intention a
// poser, et ce qu'une pose a produit.
//
// # Ils sont COMMUNS parce qu'ils l'were already, en trois copies
//
// Les trois connecteurs livres portaient chacun leur `VLAN`, leur `PortState`, leur
// `State`, leur `ApplyResult` et leur `Step`, et deux d'entre eux le disaient
// explicitement dans leurs commentaires — « same forme que celle du connecteur
// Instant On, pour que l'API n'ait qu'une way de present  une pose », « c'est ce
// qui permet a la couche de reconciliation de comparer deux plateformes sans les
// distinguer ». Trois definitions identiques par convention tiennent jusqu'au jour where
// l'une gagne un champ : ce jour-there, la conversion silencieuse de l'API perd ce champ
// sans qu'aucun test ne le voie, et un ecart de configuration devient invisible.
//
// La migration cote connecteur ne costs rien et ne demande PAS de rewrite leur
// code : un ALIAS suffit — `type PortState = driver.PortState` — et leurs champs, leurs
// fonctions pures et leurs tests continuent de compiler tels quels.
//
// # Les tags JSON sont ceux d'origine, et ce n'est pas cosmetique
//
// Deux d'entre eux traversent une frontiere que Go ne surveille pas :
//
//   - `State` est serialise dans `devices.observed` (via api.observedState) et relu
//     par tout ce qui calcule la derive de configuration. Renommer un tag ferait
//     lire « zero VLAN, aucun port » sur TOUT LE PARC already observed, ce qui se
//     presenterait comme une derive massive et declencherait des poses ;
//   - `ApplyResult`, `SNMPResult`, `TrapResult` et `Step` partent dans les responses
//     HTTP que le frontend affiche. Un tag changed n'y casse rien de visible : il
//     affiche simplement du vide, ce qui est le pire des deux.
//
// # Ce qui n'est PAS ici, et pourquoi
//
// Rien de ce qui est propre a une plateforme : ni les sections `wcd`, ni les pages
// `*.b`, ni les `.id` de RouterOS. Ce qui est propre a une plateforme mais que
// l'exploitant DOIT savoir passe par les champs de texte planned pour ca
// ([State.Notes], [ApplyResult.Skipped], [ApplyResult.Notes], [SNMPResult.Skipped], [TrapResult.Notes]) —
// des phrases written par le connecteur, affichees telles quelles, sans que la
// couche API ait a comprendre de quelle plateforme elles parlent.

// VLAN est un VLAN declared sur the device.
type VLAN struct {
	ID int `json:"id"`
	// Name est le label que the device porte, quand il en porte un.
	//
	// A NE PAS PRENDRE POUR UN IDENTIFIANT : RouterOS ne NOMME pas ses VLAN — son
	// menu de VLAN de bridge n'a pas de champ de nom, et ce qui en tient lieu est un
	// `comment` de texte libre (observed : « added by pvid » sur une ingress que
	// the device s'est created tout seul). Un connecteur remonte ce qu'il a ; un
	// appelant qui comparerait des noms pour decider d'une write se tromperait.
	Name string `json:"name"`
}

// PortState est la configuration d'UN port telle que the device la porte, ou telle
// que meshcraft la veut.
//
// Volontairement limitee a ce qui est CONFIGURE. Le rate, state du lien, la
// consommation PoE et la temperature sont de la TELEMETRY : les faire entrer ici les
// ferait comparer par le calcul de derive, et tout le parc clignoterait en
// permanence.
//
// Le same type sert en lecture et en intention, ce qui est ce qui rend le diff
// possible sans traduction — a un champ pres, flagged ci-dessous.
type PortState struct {
	// Mode : access | trunk | libre. « libre » et le vide valent « non managed » : le
	// connecteur ne touche pas a ce port, ce qui est la seule way d'adopter un
	// device sans reclamer ses quarante-huit prises.
	Mode string `json:"mode"`
	// PVID est le VLAN natif, celui dont le trafic sort SANS tag.
	PVID int `json:"pvid,omitempty"`
	// Tagged est la liste des VLAN que le port transporte etiquetes, TRIEE.
	//
	// Triee parce que deux executions doivent produire exactement la same request :
	// sinon comparer deux transcripts ne veut plus rien dire, et un diff signalerait
	// un ecart a chaque relecture.
	Tagged []int `json:"tagged,omitempty"`
	// TaggedAtLeast : do not remove tagged VLANs on this port (unresolved « all »).
	//
	// Set ONLY on intent, never on a read state. After ResolveTaggedAll, a normal
	// « all » carries the exact site VLAN list in Tagged and this flag is false —
	// apply adds and removes until the device matches. True only while the site
	// declares no VLANs yet: stripping tagged membership would be guessing.
	TaggedAtLeast bool `json:"tagged_at_least,omitempty"`
}

// State est state observe d'un device.
type State struct {
	VLANs []VLAN               `json:"vlans"`
	Ports map[string]PortState `json:"ports"`
	// MgmtVLAN est le VLAN par lequel the device se dit administrable, tel qu'IL le
	// declares — et non tel que le plan du site le souhaite.
	//
	// La distinction est vitale, pas cosmetique : un device pas encore migre vers
	// le plan est joignable par SON VLAN. Proteger celui du plan protegerait un VLAN
	// que the device n'utilise pas, en laissant rewrite celui qui porte la session
	// en cours. Constate sur un 1930 : plan a 99, device a 1.
	//
	// ZERO NE VEUT PAS DIRE LA SAME CHOSE PARTOUT, et c'est au connecteur de le
	// traduire en garde-fou, pas a l'appelant de l'interpreter. Sur l'Instant On, zero
	// est une ignorance et writing est refused. Sur SwOS, zero est un etat
	// LEGITIME — « administration accepted depuis n'importe quel VLAN », celui d'un
	// device sorti d'usine — et refuser to write empecherait d'adopter un switch
	// neuf ; la protection se reporte alors sur le seul etat des liens. Sur RouterOS,
	// zero veut dire que l'administration n'arrive pas par un VLAN tagged.
	MgmtVLAN int `json:"mgmt_vlan"`
	// LinkUp : les ports qui portent un lien.
	//
	// Un port SANS lien ne peut pas porter la session d'administration en cours :
	// c'est ce qui permet de reconfigurer un parc neuf, where tous les ports sont non
	// tagged dans le VLAN de management et where les epargner tous reviendrait a ne rien
	// pouvoir faire.
	//
	// Seuls les ports AVEC lien y figurent : l'absence vaut « pas de lien », ce qui
	// est aussi le default prudent quand la lecture n'a rien dit.
	LinkUp map[string]bool `json:"link_up,omitempty"`
	// Notes porte ce que le connecteur a compris et que ce type ne sait pas dire.
	//
	// LE CHAMP QUI EVITE DE MENTIR, et il est ne d'un fait observed sur les deux
	// nouvelles plateformes : une configuration VLAN peut be present et INERTE.
	// Sur SwOS, un port dont le mode VLAN est « disabled » ne filtre rien — son PVID
	// et son appartenance sont configures mais sans effet sur le trafic. Sur RouterOS,
	// un bridge dont `vlan-filtering` est inactif laisse tout passer partout, et un
	// port peut etiqueter en VLAN 100 sans be membre du VLAN 100 (observed sur
	// l'reference device, parfaitement accepted et silencieux).
	//
	// Sans ce champ, the UI presenterait ces cas comme une segmentation en place. Ce
	// sont des PHRASES pour un exploitant, pas des codes : l'appelant les affiche, il
	// n'en tire aucune decision — un connecteur qui voudrait qu'une decision soit
	// prise doit refuser lui-same.
	Notes []string `json:"notes,omitempty"`
}

// PortPlan est ce qu'une pose de configuration doit obtenir.
//
// Un agregat rather que cinq parameters, et pas seulement pour la lisibilite : le
// jour where la pose a besoin d'une contrainte de plus, les trois connecteurs changent
// leur signature en same temps ou pas du tout — un parameter ajoute a trois endroits
// se fait oublier au troisieme, et la pose de cette plateforme-there would write autre
// chose que ce que the UI annonce.
type PortPlan struct {
	// Desired est la configuration voulue, par NOM DE PORT du model standard.
	//
	// Un port ABSENT de cette carte n'est pas « a vider » : il est NON MANAGED, et le
	// connecteur n'y touche pas. C'est la same rule que `Mode` vide, et elle est ce
	// qui permet d'adopter un device dont on ne configure que trois prises.
	Desired map[string]PortState
	// MgmtVLAN est le VLAN de management que le PLAN DU SITE souhaite.
	//
	// Le connecteur y AJOUTE celui que the device declares ([State.MgmtVLAN]) et
	// protege les deux : ils different tant qu'un device n'a pas been migre, et ne
	// proteger que le premier laisserait rewrite celui qui porte la session.
	//
	// Zero veut dire « le site n'a pas de plan exploitable ». Ce n'est pas une
	// autorisation to write a l'aveugle : c'est au connecteur de decider si sa
	// plateforme permet encore de se proteger autrement (cf. [State.MgmtVLAN]).
	MgmtVLAN int
	// SiteVLANs sont les VLAN que le site declares, et qui doivent EXISTER sur
	// the device same si aucun port ne les utilise encore.
	//
	// Ce n'est pas du zele : un device adopte doit connaitre les VLAN de son site,
	// sans quoi y affecter un port depuis l'interface de the device — ou brancher un
	// trunk dessus — ne proposerait rien et ne transporterait rien. C'est la
	// difference entre un device « adopte » et un device « simplement
	// joignable ».
	SiteVLANs []VLAN
	// Force leve les garde-fous qui protegent les ports portant l'administration.
	//
	// JAMAIS enabled par default, ni par la pose automatique : c'est un geste explicite
	// de l'exploitant, qui connait sa topologie mieux que meshcraft et accepte le
	// risque de devoir se deplacer. Un connecteur qui l'ignorerait rendrait le bouton
	// « forcer » de the UI inoperant sans le dire ; un connecteur qui forcerait sans
	// qu'on le demande n'aurait plus de garde-fou du tout.
	Force bool
}

// ApplyResult dit ce qu'une pose a fait, et surtout ce qu'elle n'a pas fait.
type ApplyResult struct {
	// VLANsCreated : les VLAN qu'il a fallu create avant d'y affecter des ports.
	VLANsCreated []int `json:"vlans_created"`
	// PortsWritten : les ports actually reconfigures. Un port already conforme n'y
	// figure pas — ne rien envoyer vaut mieux qu'un aller-retour.
	PortsWritten []string `json:"ports_written"`
	// Skipped : ce que meshcraft a REFUSED de faire, avec la raison — decisions
	// de security que l'exploitant peut lever en forcant (port d'administration…).
	//
	// LE CHAMP LE PLUS IMPORTANT DE LA STRUCTURE : un apply partiel presented comme
	// complet laisserait croire a une configuration qui n'est pas en place.
	//
	// A ne pas confondre avec Notes (faits sans decision : offload perdu…) ni avec
	// ce que la plateforme ne sait pas WRITE (cf. [Connector.WritesExtra]) :
	// melanger proposerait un bouton « forcer » qui ne changerait rien.
	Skipped []string `json:"skipped,omitempty"`
	// Notes : faits d'apply qui demandent d'etre dits et aucune decision.
	Notes []string `json:"notes,omitempty"`
	// Backup est la configuration de the device AVANT write, telle que la
	// plateforme l'a rendue, ou vide.
	//
	// Existe pour une plateforme et une raison : SwOS applique et persiste d'un seul
	// geste, une coupure d'alimentation n'annule rien, et cette copie est le SEUL
	// retour arriere qui existe. Le connecteur la prend juste avant la first
	// write ; elle ne se reconstitue pas after.
	//
	// Vide n'est pas un manque : sur une plateforme en `apply_then_save`, le filet est
	// ailleurs — ne pas persister (cf. [Persister]). Ce que l'appelant en fait est de
	// sa responsabilite, mais la laisser tomber en silence reviendrait a annoncer un
	// filet qu'on a jete.
	Backup string `json:"backup,omitempty"`
}

// --- SNMP ---

// SNMPIdentity est identity SNMP a poser, telle que `snmpprov` l'a decidee.
//
// Les halves sont INDEPENDANTES : un champ vide veut dire « rien a faire ici »,
// jamais « retirer ». Une pose n'est pas une factory reset, et un connecteur qui
// effacerait ce qu'on ne lui demande pas de poser couperait la supervision d'un tiers.
type SNMPIdentity struct {
	// Community : la community v2c, TOUJOURS applied en LECTURE SEULE. Vide = pas de
	// v2c.
	//
	// meshcraft n'a aucun moyen d'en demander une en write, et c'est deliberate : une
	// community RW sur un parc, c'est un mot de passe root en clair sur le network. Il
	// does not write d'ailleurs RIEN par SNMP — toute sa configuration passe par le
	// connecteur.
	Community string
	// User : l'utilisateur v3. Vide = pas de v3. Exclusif avec Community dans les
	// faits, sans que le contrat l'impose : c'est le plan qui decide, et une
	// plateforme peut legitimately porter les deux.
	User string
	// AuthAlg / PrivAlg : les noms d'algorithmes, sous n'importe quelle forme
	// (« SHA-1 », « sha », « DES »). Le connecteur les normalise et REFUSE ce que sa
	// plateforme n'a pas — jamais le « plus proche ».
	AuthAlg string
	PrivAlg string
	// AuthSecret / PrivSecret : les mots de passe hence the device derivera ses keys.
	//
	// DEUX phrases DISTINCTES, et ce n'est pas une precaution de style : reutiliser la
	// same des deux cotes fait entrer exactement la same valeur dans les deux
	// derivations de la RFC 3414, si bien que la key de chiffrement n'est plus que le
	// prefix de la key d'authentification — le chiffrement n'ajoute alors rien. Sans
	// seconde phrase, on NE POSE PAS de v3.
	//
	// Ils partent EN CLAIR dans le corps de la request, comme le font les interfaces
	// de ces devices. La confidentialite repose sur TLS quand il y en a, et sur le
	// tunnel du proxy toujours.
	AuthSecret string
	PrivSecret string
	// Group : le groupe access du v3. Vide = le default du connecteur.
	Group string
	// Sources borne les adresses autorisees a interroger l'agent, telles que la
	// plateforme les exprime (une liste de prefixes).
	//
	// CHAMP NE D'UN REFUS DE ROUTEROS, et il faut le lire comme tel : sur cette
	// plateforme, une community porte un champ `addresses` et le connecteur REFUSE de
	// le laisser vide — « laisser SNMP ouvert a toutes les adresses doit be requested
	// explicitement ». Le observed montre `::/0` sur l'reference device, donc
	// TOUTES les sources, y compris hors du VLAN de management. C'est un reglage, pas
	// une contrainte, et le laisser ouvert est un choix qu'un exploitant devrait faire
	// sciemment.
	//
	// La difference de nature avec l'Instant On est instructive : cette gamme confine
	// SNMP au VLAN d'administration par construction, il n'y a rien a borner. Un
	// connecteur dont la plateforme n'a pas ce reglage IGNORE ce champ.
	//
	// VIDE VEUT DIRE « L'APPELANT N'A RIEN DIT », et surtout pas « toutes ». Un
	// connecteur qui ne peut pas poser sans borne doit refuser en nommant le geste
	// manquant, rather qu'inventer un default que personne n'a choisi.
	Sources []string
}

// SNMPResult dit ce qui a been fait, et surtout ce qui ne l'a pas been.
type SNMPResult struct {
	// Enabled : l'agent SNMP est actif a l'issue de l'operation.
	//
	// Poser une identity sur un agent off donnerait une supervision configuree et
	// muette, ce qui est le pire des deux mondes : rien ne repond, et rien ne dit
	// pourquoi. Le connecteur l'allume donc au passage.
	Enabled bool `json:"enabled"`
	// CommunitySet / UserSet / GroupCreated : une write a bien eu lieu ET a been
	// verified. FAUX quand the device was already conforme — repasser sur un parc
	// already qualifie ne doit rien rewrite.
	CommunitySet bool `json:"community_set"`
	UserSet      bool `json:"user_set"`
	GroupCreated bool `json:"group_created"`
	// Skipped : ce que meshcraft a REFUSED de faire, avec la raison.
	Skipped []string `json:"skipped,omitempty"`
}

// --- Destination des traps ---

// TrapDestination est le destinataire a declare : where le collecteur ecoute, et sous
// quelle identity the device doit s'annoncer.
type TrapDestination struct {
	// Address est l'adresse du collecteur, i.e. du proxy.
	//
	// ELLE SE DEMANDE, ELLE NE SE DEDUIT PAS, et c'est la responsabilite de
	// l'appelant : seul le noyau du proxy a la table de routage qui dit par quelle
	// adresse il joint cet device. Chercher, parmi les adresses que le proxy
	// declares, celle du sous-network de the device est faux from qu'il l'atteint par
	// routage — observed sur le parc de labo, where aucune des adresses du proxy n'est
	// sur le network de the device.
	//
	// A NOTER, parce que ca se paie cher : l'adresse SOURCE des traps, elle, n'est
	// generally pas un choix. Sur l'Instant On il n'y a pas de `source-interface`
	// SNMP et une seule interface IP exploitable — les traps partent de l'adresse de
	// management. Deplacer le VLAN de management change donc l'emetteur vu par le
	// proxy et invalide silencieusement sa liste de surveillance.
	Address string
	// Port : 0 vaut le port standard 162.
	//
	// Configurable parce que les plateformes le permettent (observed sur l'Instant On :
	// une saisie ordinaire validee sur 1–65535), ce qui dispense le proxy d'ecouter
	// sur un port privilegie. Un connecteur dont la plateforme n'a pas ce champ
	// REFUSE une valeur autre que 0 ou 162 rather que de l'ignorer : accepter un port
	// qu'on n'will write pas ferait attendre des traps sur un port muet.
	Port int
	// Community : la community dont les traps v2c seront marques. Exclusive avec
	// User.
	//
	// CONTRAINTE QUI VIENT DU COLLECTEUR, PAS DE THE DEVICE : le proxy n'accepte un
	// trap que si la community qu'il porte egale celle avec laquelle il INTERROGE
	// the device. Une community de traps distincte, same dediee, same « propre »,
	// ferait rejeter 100 % des traps du parc — silencieusement, avec pour seul temoin
	// un compteur en DEBUG sur le proxy. Les devices, eux, ne l'exigent pas : sur
	// l'Instant On le champ est un texte libre que le firmware ne rapproche d'aucune
	// ingress de sa liste de communautes.
	Community string
	// User : l'utilisateur SNMPv3 sous lequel signer. Exclusif avec Community.
	//
	// Il doit EXISTER sur the device, et c'est au connecteur de le verify parce
	// que le firmware ne le fait pas : le champ est un texte libre qu'il n'oppose a
	// rien. Un destinataire v3 mal named s'enregistre sans erreur et n'emet rien
	// d'authentifiable — un silence qu'aucun UI ne distingue d'un network calme.
	User string
}

// TrapResult dit ce qui a been fait, et ce qui n'a pas pu be verified.
type TrapResult struct {
	// Set : une write a eu lieu ET a been confirmee par relecture. Faux quand
	// the device portait already le bon destinataire.
	Set bool `json:"set"`
	// Version : « v2c » ou « v3 ».
	Version string `json:"version"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	// Replaced : un destinataire divergent portant la same adresse a been retire avant
	// la pose. Concerne les plateformes dont identity fait partie de la KEY de
	// ingress : un `set` portant un autre nom y ajouterait une ligne au lieu de
	// remplacer celle qui est there, et l'ancienne — inerte — continuerait d'occuper une
	// place dans une table dont le plafond est inconnu.
	Replaced bool `json:"replaced"`
	// Known : combien de destinataires the device portait AVANT writing.
	//
	// Ce n'est pas de la statistique. Le nombre de destinataires que ces gammes
	// acceptent n'est documente nulle part, et le firmware ne l'applique only
	// l'AJOUT : ce compteur est la seule mesure qu'on en aura jamais — celle du
	// dernier ajout accepted, et celle du premier refused.
	Known int `json:"known"`
	// Notes : ce que le connecteur n'a PAS pu verify.
	//
	// Un destinataire applied sur un device dont l'transmit de traps est peut-be
	// coupee n'est pas une supervision : le dire vaut mieux que de le laisser croire.
	// C'est aussi ici que va ce qui est propre a une plateforme et que l'exploitant
	// doit savoir — deux tables distinctes sur l'Instant On, un generateur de traps
	// limite aux alertes de temperature sur RouterOS.
	Notes []string `json:"notes,omitempty"`
}

// --- Identification ---

// Identity est ce que le connecteur a pu establish de the device a une adresse.
//
// PARTIELLE PAR CONSTRUCTION, et c'est le point a retenir : un connecteur rend ce
// qu'il a PU lire same quand il s'est stopped en chemin, et l'erreur qui accompagne
// dit pourquoi. « C'est un CSS326-24G-2S+ dont on n'a pas le mot de passe » est un
// result utile ; « failure » n'en est pas un.
//
// Platform-specific fields do not belong here — version du chargeur
// boot, revision de carte, index de la first cage optique. Les connecteurs
// gardent leur propre type identity, plus riche, et remplissent celui-ci pour la
// couche API. C'est le sens de la circulation : le contrat porte ce que l'APPELANT
// consomme.
type Identity struct {
	// Model est la reference constructeur, telle que the device se nomme.
	Model string `json:"model"`
	// ModelCode est la reference catalogue quand elle differe du label (« JL685A »).
	ModelCode string `json:"model_code,omitempty"`
	// Family est la sous-gamme (« 1930 », « CSS »), utile aux differences de
	// capabilities qui se jouent a ce niveau.
	Family string `json:"family,omitempty"`
	// Serial est le serial number. C'EST LE SEUL CHAMP QUI FASSE FOI POUR RECONNAITRE
	// UN DEVICE A UNE AUTRE ADRESSE : la relocalisation ne retient une adresse que
	// si la serial concorde, sinon meshcraft pointerait vers un autre device.
	Serial   string `json:"serial,omitempty"`
	MAC      string `json:"mac,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	Build    string `json:"build,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	// Facade : ce qui permet de dessiner the device sans catalogue de modeles.
	//
	// A l'adoption, un device demarre vierge dans le model — mais ses ports
	// EXISTENT. Sans reprise de la front panel observed, l'adoption produisait un device
	// SANS AUCUN PORT, ce qui n'est pas « vierge » mais faux.
	CopperPorts int  `json:"copper_ports,omitempty"`
	SFPPorts    int  `json:"sfp_ports,omitempty"`
	SFPPlus     bool `json:"sfp_plus,omitempty"`
	PoE         bool `json:"poe,omitempty"`
	// PortGroups est la front panel dans la forme que le frontend consomme already
	// (`prefix`, `count`, `from`, `speed`, `poe`, `label`).
	//
	// Rendue par le connecteur et recopiee sans traduction, deliberately : la
	// numerotation est un savoir de plateforme. Sur l'Instant On, les cages optiques
	// continuent la numerotation du cuivre (48 ports → cages de 49 a 52) ; sur
	// RouterOS, les noms meshcraft SONT les noms de the device, donc les groupes
	// portent plusieurs prefixes (`ether`, `sfp-sfpplus`) et sont verified CONTIGUS —
	// annoncer « 24 ports a partir de 1 » quand il en manque un au milieu ferait
	// dessiner une prise qui n'existe pas, et meshcraft finirait par write dessus.
	PortGroups []map[string]any `json:"port_groups,omitempty"`
	// Source dit D'WHERE vient l'information : the UI ne doit pas present  une
	// deduction comme une lecture. Les valeurs sont celles du connecteur
	// (`login_page`, `provisioning_api`, `digest_realm`, `rest_api`), et leur
	// heterogeneite est voulue — c'est la seule way qu'un sceptique retrouve le
	// observed.
	Source string `json:"source,omitempty"`
}

// OnboardResult est ce qu'une bascule en administration locale a produit.
//
// L'important n'est pas le success annonce mais la comparaison AVANT / AFTER : un 2xx
// qui ne change rien est un failure silencieux, et c'est le mode failure qu'on cherche
// a attraper.
type OnboardResult struct {
	// Was / Stage : state du mode de gestion avant et after, avec les mots du
	// connecteur.
	Was   string `json:"was"`
	Stage string `json:"stage"`
	// Changed : les deux different. C'est ca, et rien d'autre, qui prouve que le geste
	// a porte.
	Changed bool `json:"changed"`
	// Identity est ce qu'on a appris de the device au passage. La bascule lit son
	// etat de toute way ; le rendre evite un second aller-retour au moment precise where
	// l'adoption a besoin du model et de la serial.
	Identity Identity `json:"identity"`
	// Detail est la response de the device, VERBATIM.
	//
	// Sur une plateforme dont le geste write a been reconstitue et non documente,
	// c'est la seule chose qui permette de comprendre un refus sans retourner sur
	// site. Rendu tel quel rather qu'interprete : l'interpreter demanderait de
	// connaitre d'avance les responses possibles, ce qui est exactement ce qu'on n'a
	// pas.
	Detail string `json:"detail,omitempty"`
	// Steps est le transcript du geste. Une bascule est une WRITE durable — sur la
	// gamme connue, seul un retour aux reglages d'usine la defait — et elle merite la
	// same trace qu'une pose de configuration.
	Steps []Step `json:"steps,omitempty"`
}

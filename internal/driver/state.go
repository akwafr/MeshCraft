package driver

// The types the API layer exchanges with a connector: state read, the intent to
// set, and what a write produced.
//
// # They are SHARED because they already were, in three copies
//
// The three shipped connectors each carried their own `VLAN`, `PortState`,
// `State`, `ApplyResult`, and `Step`, and two of them said so explicitly in their
// comments — "same shape as the Instant On connector's, so the API has only one
// way to present a write," "this is what lets the reconciliation layer compare
// two platforms without telling them apart." Three identical definitions kept in
// sync by convention hold up only until the day one of them gains a field: that
// day, the API's silent conversion drops that field without any test noticing,
// and a configuration drift becomes invisible.
//
// The migration on the connector side costs nothing and does NOT require
// rewriting their code: an ALIAS is enough — `type PortState = driver.PortState`
// — and their fields, their pure functions, and their tests keep compiling as-is.
//
// # The JSON tags are the original ones, and this is not cosmetic
//
// Two of them cross a boundary Go does not watch over:
//
//   - `State` is serialized into `devices.observed` (via api.observedState) and
//     read back by everything that computes configuration drift. Renaming a tag
//     would make it read "zero VLANs, no ports" for the ENTIRE FLEET already
//     observed, which would look like a massive drift and trigger writes;
//   - `ApplyResult`, `SNMPResult`, `TrapResult`, and `Step` go into the HTTP
//     responses the frontend displays. A changed tag breaks nothing visibly
//     there: it simply displays emptiness, which is the worse of the two
//     failure modes.
//
// # What is NOT here, and why
//
// Nothing platform-specific: no `wcd` sections, no `*.b` pages, no RouterOS
// `.id`s. What is platform-specific but that the operator MUST know goes through
// the text fields planned for that purpose ([State.Notes], [ApplyResult.Skipped],
// [ApplyResult.Notes], [SNMPResult.Skipped], [TrapResult.Notes]) — sentences
// written by the connector, displayed as-is, without the API layer having to
// understand which platform they're talking about.

// VLAN is a VLAN declared on the device.
type VLAN struct {
	ID int `json:"id"`
	// Name is the label the device carries, when it carries one.
	//
	// DO NOT TREAT AS AN IDENTIFIER: RouterOS does not NAME its VLANs — its
	// bridge VLAN menu has no name field, and what stands in for one is a
	// free-text `comment` (observed: "added by pvid" on an entry the device
	// created entirely on its own). A connector reports back whatever it has; a
	// caller that compared names to decide on a write would be making a
	// mistake.
	Name string `json:"name"`
}

// PortState is the configuration of ONE port as the device carries it, or as
// meshcraft wants it.
//
// Deliberately limited to what is CONFIGURED. Rate, link state, PoE draw, and
// temperature are TELEMETRY: bringing them in here would make them get compared
// by the drift calculation, and the whole fleet would blink constantly.
//
// The same type serves both reads and intents, which is what makes diffing
// possible without translation — with one field's exception, flagged below.
type PortState struct {
	// Mode: access | trunk | empty. "empty" (and the zero value) both mean "not
	// managed": the connector leaves this port alone, which is the only way to
	// adopt a device without claiming all forty-eight of its jacks.
	Mode string `json:"mode"`
	// PVID is the native VLAN, the one whose traffic leaves WITHOUT a tag.
	PVID int `json:"pvid,omitempty"`
	// Tagged is the list of VLANs the port carries tagged, SORTED.
	//
	// Sorted because two runs must produce exactly the same request: otherwise
	// comparing two transcripts stops meaning anything, and a diff would report
	// a discrepancy on every re-read.
	Tagged []int `json:"tagged,omitempty"`
	// TaggedAtLeast: do not remove tagged VLANs on this port (unresolved "all").
	//
	// Set ONLY on intent, never on a read state. After ResolveTaggedAll, a normal
	// "all" carries the exact site VLAN list in Tagged and this flag is false —
	// apply adds and removes until the device matches. True only while the site
	// declares no VLANs yet: stripping tagged membership would be guessing.
	TaggedAtLeast bool `json:"tagged_at_least,omitempty"`
}

// State is the observed state of a device.
type State struct {
	VLANs []VLAN               `json:"vlans"`
	Ports map[string]PortState `json:"ports"`
	// MgmtVLAN is the VLAN through which the device says it is administrable,
	// as IT declares it — not as the site plan wants it to be.
	//
	// The distinction is vital, not cosmetic: a device not yet migrated to the
	// plan is reachable through ITS OWN VLAN. Protecting the plan's VLAN would
	// protect a VLAN the device isn't using, while letting the one carrying the
	// current session be overwritten. Observed on a 1930: plan says 99, device
	// says 1.
	//
	// ZERO DOES NOT MEAN THE SAME THING EVERYWHERE, and it's up to the connector
	// to translate it into a safeguard, not up to the caller to interpret it. On
	// Instant On, zero means ignorance and writes are refused. On SwOS, zero is
	// a LEGITIMATE state — "administration accepted from any VLAN," the state
	// of a device fresh from the factory — and refusing to write would prevent
	// adopting a new switch; protection then falls back to link state alone. On
	// RouterOS, zero means administration is not arriving through a tagged
	// VLAN.
	MgmtVLAN int `json:"mgmt_vlan"`
	// Links: the OPERATIONAL STATE of each port, richer than a link/no-link bool.
	//
	// The previous model was `LinkUp map[string]bool`, and it conflated three
	// facts that call for opposite reactions: a port with nothing plugged in, a
	// port an operator shut on purpose, and a port the SWITCH shut itself
	// (err-disabled — a refused optic, a bpduguard trip, a security violation).
	// All three read as "no link" and only one is a problem. This carries the
	// distinction the connectors already observe.
	//
	// [LinkUp] and [HasLink] preserve the old meaning for the apply guardrail:
	// only a port that is genuinely UP can carry the current administration
	// session, and that is what allows reconfiguring a brand-new fleet where
	// every port is untagged in the management VLAN. A missing entry means "not
	// read / unknown", the cautious default.
	Links map[string]LinkState `json:"links,omitempty"`
	// Notes carries what the connector understood that this type cannot
	// otherwise express.
	//
	// THE FIELD THAT AVOIDS LYING, and it was born from a fact observed on the
	// two newer platforms: a VLAN configuration can be PRESENT and INERT. On
	// SwOS, a port whose VLAN mode is "disabled" filters nothing — its PVID and
	// membership are configured but have no effect on traffic. On RouterOS, a
	// bridge whose `vlan-filtering` is off lets everything through everywhere,
	// and a port can tag VLAN 100 without being a member of VLAN 100 (observed
	// on the reference device, perfectly accepted and silent).
	//
	// Without this field, the UI would present these cases as segmentation
	// actually in place. These are SENTENCES for an operator, not codes: the
	// caller displays them, and draws no decision from them — a connector that
	// wants a decision to be made must refuse on its own.
	Notes []string `json:"notes,omitempty"`
}

// LinksFromUp converts an "up ports" boolean map — the shape several connectors
// build internally — into the shared [LinkState] vocabulary. Only ports present
// and true become [LinkUpState]; everything else is simply absent, meaning
// "unknown / no link", exactly as the old boolean map behaved. A platform that
// can distinguish disabled from unplugged builds the richer map directly instead.
func LinksFromUp(up map[string]bool) map[string]LinkState {
	if up == nil {
		return nil
	}
	var out = map[string]LinkState{}
	var name string
	var v bool
	for name, v = range up {
		if v {
			out[name] = LinkUpState
		}
	}
	return out
}

// LinkState is a port's operational state, as the fleet's switches actually
// report it. Deliberately a small, closed vocabulary shared by every connector:
// a platform that cannot tell two of these apart reports the coarser one rather
// than inventing a distinction it did not observe.
type LinkState string

const (
	// LinkUpState: a forwarding link is present. The only state that carries an
	// administration session, and therefore the only one the apply guardrail
	// treats as "do not cut".
	LinkUpState LinkState = "up"
	// LinkDown: the port is down for a reason the connector could not narrow to
	// one of the more specific states below. The honest fallback.
	LinkDown LinkState = "down"
	// LinkDisabled: administratively shut (« shutdown », « disable »). Down
	// because someone chose it, not because anything is wrong.
	LinkDisabled LinkState = "disabled"
	// LinkUnplugged: no cable / no signal (« notconnect »). Down because nothing
	// is connected — not a fault, and not a configuration to worry about.
	LinkUnplugged LinkState = "unplugged"
	// LinkErrDisabled: the SWITCH shut the port itself. A refused transceiver, a
	// bpduguard trip, a port-security violation — always something to look at,
	// and the one state that must be surfaced on the interface rather than
	// hidden inside "no link".
	LinkErrDisabled LinkState = "err-disabled"
)

// HasLink reports whether a port carries a usable link — the apply guardrail's
// question, and the exact meaning the old `LinkUp[name]` map carried. True only
// for [LinkUpState]: a disabled, unplugged or err-disabled port carries no
// session and may be reconfigured.
func (s State) HasLink(name string) bool {
	return s.Links[name] == LinkUpState
}

// LinkUp rebuilds the old boolean map, for the persisted DTO and for callers
// that only need "does this port have a link". One entry per UP port, matching
// the previous field's semantics exactly so `devices.observed` and conformity
// checks keep reading the same thing.
func (s State) LinkUp() map[string]bool {
	if s.Links == nil {
		return nil
	}
	var out = map[string]bool{}
	var name string
	var st LinkState
	for name, st = range s.Links {
		if st == LinkUpState {
			out[name] = true
		}
	}
	return out
}

// PortPlan is what a configuration write must achieve.
//
// An aggregate rather than five parameters, and not just for readability: the
// day the write needs one more constraint, the three connectors change their
// signature together or not at all — a parameter added in three places gets
// forgotten at the third, and that platform's write would set something other
// than what the UI advertises.
type PortPlan struct {
	// Desired is the wanted configuration, keyed by PORT NAME in the standard
	// model.
	//
	// A port ABSENT from this map is not "to be cleared": it is NOT MANAGED,
	// and the connector leaves it alone. Same rule as an empty `Mode`, and it's
	// what allows adopting a device on which only three jacks are configured.
	Desired map[string]PortState
	// MgmtVLAN is the management VLAN the SITE PLAN wants.
	//
	// The connector ADDS to it the one the device declares ([State.MgmtVLAN])
	// and protects both: they differ as long as a device hasn't been migrated
	// yet, and protecting only the first would let the one carrying the
	// session get overwritten.
	//
	// Zero means "the site has no actionable plan." This is not authorization
	// to write blindly: it's up to the connector to decide whether its
	// platform can still protect itself some other way (cf. [State.MgmtVLAN]).
	MgmtVLAN int
	// SiteVLANs are the VLANs the site declares, which must EXIST on the
	// device even if no port uses them yet.
	//
	// This isn't overzealousness: an adopted device must know its site's
	// VLANs, or assigning a port to one from the device's own interface — or
	// plugging a trunk into it — would offer nothing and carry nothing. This
	// is the difference between a device that is "adopted" and one that is
	// "merely reachable."
	SiteVLANs []VLAN
	// Force lifts the safeguards that protect ports carrying administration.
	//
	// NEVER enabled by default, nor by an automatic write: it's an explicit
	// gesture by the operator, who knows their topology better than meshcraft
	// and accepts the risk of having to travel on site. A connector that
	// ignored it would make the UI's "force" button silently useless; a
	// connector that forced without being asked would have no safeguard left
	// at all.
	Force bool
}

// ApplyResult says what a write did, and above all what it did NOT do.
type ApplyResult struct {
	// VLANsCreated: the VLANs that had to be created before ports could be
	// assigned to them.
	VLANsCreated []int `json:"vlans_created"`
	// PortsWritten: the ports actually reconfigured. A port already compliant
	// does not appear here — sending nothing is better than a round trip that
	// changes nothing.
	PortsWritten []string `json:"ports_written"`
	// Skipped: what meshcraft REFUSED to do, with the reason — safety
	// decisions the operator can lift by forcing (an administration port,
	// etc.).
	//
	// THE MOST IMPORTANT FIELD OF THE STRUCT: a partial apply presented as
	// complete would suggest a configuration that isn't actually in place.
	//
	// Not to be confused with Notes (facts with no decision behind them: lost
	// offload, etc.) nor with what the platform cannot WRITE at all
	// (cf. [Connector.WritesExtra]): mixing them up would offer a "force"
	// button that would change nothing.
	Skipped []string `json:"skipped,omitempty"`
	// Notes: apply facts that need to be stated, with no decision attached.
	Notes []string `json:"notes,omitempty"`
	// Backup is the device's configuration BEFORE the write, exactly as the
	// platform returned it, or empty.
	//
	// Exists for one platform and one reason: SwOS applies and persists in a
	// single gesture, a power outage undoes nothing, and this copy is the ONLY
	// way back that exists. The connector takes it right before the first
	// write; it cannot be reconstructed afterward.
	//
	// Empty is not a gap: on a platform running `apply_then_save`, the safety
	// net is elsewhere — not persisting (cf. [Persister]). What the caller
	// does with it is the caller's responsibility, but letting it drop
	// silently would amount to advertising a safety net that was thrown away.
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

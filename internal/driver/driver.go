// Package driver definit le contrat des connecteurs de plateforme et le
// registre qui les recense.
//
// Un connecteur sait parler a UNE plateforme : il traduit le model de data
// standard vers ce que the device comprend, et l'inverse en lecture. Voir
// docs/01-Architecture/Connecteurs.
//
// Ce paquet ne connait ni SSH ni HTTP : un connecteur receives une Session dont le
// transport est already established — directement ou a travers un [[Proxy]] — et n'a
// aucune notion de tunnel. C'est la rule « un seul chemin de code ».
package driver

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrNotImplemented : la capability existe dans le contrat mais pas encore dans ce
// connecteur. Le dire explicitement vaut mieux qu'un comportement vide qui
// laisserait croire a un success.
var ErrNotImplemented = errors.New("non implemented par ce connecteur")

// Info identifie le connecteur.
type Info struct {
	// Name est l'identifiant stable used en base et dans l'API.
	Name   string `json:"name"`
	Vendor string `json:"vendor"`
	// Family describes la gamme couverte, telle qu'un operator la nomme.
	Family string `json:"family"`
	// Transport : ce par quoi the device s'administre actually.
	Transport []string `json:"transport"` // web | ssh | telnet | snmp | api
	// Doc : la reference constructeur hence viennent les capabilities.
	Doc string `json:"doc,omitempty"`
}

// VlanSupport dit quels VLAN l'interface d'administration accepte.
type VlanSupport string

const (
	// VlanAny : le VLAN de management est configurable librement.
	VlanAny VlanSupport = "any"
	// VlanDefaultOnly : management sur le VLAN par default (1) uniquement.
	VlanDefaultOnly VlanSupport = "default_only"
	// VlanUnknown : on ne sait pas — donc on suppose le pire (cf. la rule
	// « une capability inconnue est supposee absente »).
	VlanUnknown VlanSupport = ""
)

// MgmtCapabilities describes ce que l'interface d'administration accepte. C'est la
// capability la plus dangereuse a supposer : se tromper rend the device
// injoignable (cf. docs/01-Architecture/Adressage de management).
type MgmtCapabilities struct {
	VlanIDs VlanSupport `json:"vlan_ids"`
	// VlanRange borne les identifiants admis pour le management.
	VlanRange [2]int `json:"vlan_range"`
	// TaggedOK : le management fonctionne sur un VLAN tagged.
	TaggedOK bool `json:"tagged_ok"`
	// Static / DHCP : modes d'adressage admis.
	Static bool `json:"static"`
	DHCP   bool `json:"dhcp"`
	// Gateway / DNS : la plateforme porte-t-elle une PASSERELLE et des RESOLVEURS
	// pour son administration ?
	//
	// Ces deux champs ne sont pas decoratifs, et ils ne se deduisent surtout pas de
	// `Static`. Le observed qui les a fait naitre : le jeu de champs inscriptible de
	// /sys.b sur un CSS326-24G-2S+ — enumerated par sa propre sauvegarde de
	// configuration, donc exhaustif — contient bien `ip` et `iptp` mais AUCUNE
	// passerelle et AUCUN resolveur. Sans passerelle, les sorties hors sous-network
	// (traps, DNS) n'ont pas de route ici ; l'administration entrante peut encore
	// repondre en L2. C'est un fait d'exploitation, pas une condamnation.
	//
	// FAUX PAR DEFAULT, comme tout ce qui touche a access. Un UI qui offre trois
	// champs de saisie sur une plateforme qui n'en porte qu'un fait saisir une
	// passerelle qui n'atterrira nulle part, et l'exploitant en conclut que meshcraft
	// n'a pas written ce qu'il avait requested. Dire « cette plateforme n'a pas de
	// passerelle de management » est une information ; afficher un champ vide n'en est
	// pas une.
	Gateway bool `json:"gateway"`
	DNS     bool `json:"dns"`
	// Routed: can management leave its own subnet (default gateway / L3)?
	//
	// Distinct from [Gateway] only in wording for the UI: Gateway is "does the
	// platform expose a gateway field?", Routed is the exploitation fact
	// "off-subnet outbound has a route". Today they match on every shipped
	// connector; both stay so the screen can say MgmtRouted: no without prose.
	Routed bool `json:"routed"`
	// IPARP: the device still answers ARP / L2 for its management IP when it has
	// no gateway — inbound admin from another subnet can work if an L2 return
	// path exists. SwOS is the reference: no gateway, but L2 reply still works.
	IPARP bool `json:"ip_arp"`
	// DHCPFallback : adresse de repli si le DHCP ne repond pas.
	DHCPFallback string `json:"dhcp_fallback,omitempty"`
	// OutOfBand : il existe une voie d'administration HORS DU NETWORK — console
	// serial, MAC-winbox, port de gestion dedie — par laquelle on recupere un
	// device qu'une erreur d'adressage a rendu muet.
	//
	// LE CHAMP QUI DECIDE DE LA GRAVITE, et il est ne d'un message FAUX. La couche
	// API refusait une bascule de VLAN de management en affirmant « cette gamme n'a
	// ni SSH ni serial port : le seul recours serait un retour aux reglages d'usine,
	// sur site ». It was vrai des deux gammes web-only livrees, et faux de
	// RouterOS — qui a une console serial ET MAC-winbox, les deux RELEVES. Tout un
	// refus was donc calibre sur une plateforme irrecuperable, et recite tel quel
	// a une plateforme qui, elle, se rattrape.
	//
	// A NE PAS DEDUIRE D'[Info.Transport] : cette liste dit par quoi meshcraft
	// administre, pas ce qui reste quand le network est perdu. Un connecteur peut
	// parler web a un device qui a aussi un serial port (RouterOS), et un autre
	// annoncer `ssh` sur une plateforme dont le SSH passe par le same VLAN que tout
	// le reste — donc tombe avec lui. Les deux erreurs vont dans le sens dangereux.
	//
	// FAUX PAR DEFAULT, et c'est la rule du projet appliquee a son cas le plus
	// couteux : une capability inconnue est supposee ABSENTE. Se tromper dans ce
	// sens fait afficher un avertissement trop dur, ce qui costs de la prudence ;
	// se tromper in l'other does tenter une manœuvre irreversible on un
	// device qu'on croyait rattrapable, ce qui costs un deplacement avec un
	// trombone.
	OutOfBand bool `json:"out_of_band"`
	// ApplySameVLAN: the connector has proven [MgmtAddrWriter] on hardware for
	// same-VLAN DHCP/static/gateway/DNS apply. FALSE by default until proven.
	ApplySameVLAN bool `json:"apply_same_vlan"`
	// MigrateVLAN: the connector implements [MgmtMover] (dual-path prepare /
	// addressed / commit). Instant On is the reference implementation.
	MigrateVLAN bool `json:"migrate_vlan"`
	// JumpVLAN: the connector implements [MgmtJumper] — honest single-address leap
	// with backup. SwOS-shaped; never set together with MigrateVLAN as a substitute
	// for dual-path on a platform that can do both.
	JumpVLAN bool `json:"jump_vlan"`
	// Notes porte les contraintes d'ORDRE et les pieges documentes par le
	// constructeur — ce qui fait la difference entre un apply qui marche et un
	// device perdu.
	Notes []string `json:"notes,omitempty"`
}

// AdminAccount describes ce que la plateforme accepte comme COMPTE
// d'administration. meshcraft veut poser partout le same nom (`mcadmin`) pour
// que access direct soit predictable ; beaucoup de switchs ingress de gamme
// n'admettent que `admin`. Cette capability est ce qui empeche de pousser un
// nom que the device refusera — et de croire ensuite qu'il l'a pris.
type AdminAccount struct {
	// Custom : la plateforme sait create un compte au nom choisi.
	Custom bool `json:"custom"`
	// FixedName : le seul nom possible quand Custom est faux.
	FixedName string `json:"fixed_name,omitempty"`
	// PasswordMin / PasswordMax : bornes imposees par the device. 0 = inconnu.
	PasswordMin int `json:"password_min,omitempty"`
	PasswordMax int `json:"password_max,omitempty"`
	// AlphanumericOnly : the device refuse la ponctuation. C'est une des
	// raisons pour lesquelles meshcraft n'engendre que de l'alphanumerique
	// (cf. internal/pwgen).
	AlphanumericOnly bool `json:"alphanumeric_only,omitempty"`
	// Notes porte ce qui ne se code pas : ordre des operations, pieges.
	Notes []string `json:"notes,omitempty"`
}

// MonitorChannel : un canal de collecte, avec son plancher de cadence.
//
// MinInterval est un PLANCHER, pas un objectif : « jamais plus souvent que ».
// Un parc de deux cents devices interroges au plancher, c'est des dizaines
// de requests par seconde en permanence, dont l'essentiel ne change jamais — et
// le CPU de supervision d'un switch access est modeste. Un device qu'on
// supervise trop est un device qu'on abime.
type MonitorChannel struct {
	// Kind : web | snmp | streaming.
	Kind string `json:"kind"`
	// Metrics : ce que ce canal sait rendre, named comme dans le model standard
	// (if_state, if_counters, health, lldp…).
	Metrics []string `json:"metrics"`
	// MinInterval : l'intervalle le plus COURT admissible sur ce canal.
	MinInterval time.Duration `json:"min_interval"`
	// Note explique une limite que la structure ne dit pas — par exemple un access
	// restreint au VLAN de management.
	Note string `json:"note,omitempty"`
}

// MetricUnavailable declares une metrique que la PLATEFORME n'expose pas, avec
// son motif — l'absence dite a voix haute rather que laissee au silence.
//
// A ne pas confondre avec une metrique que meshcraft ne collecte pas ENCORE. Les
// deux se lisent de la same way a the UI — pas de courbe — et menent a des
// decisions OPPOSEES :
//
//   - « meshcraft ne le collecte pas encore » est un manque de notre cote : il se
//     comble par une version, et l'exploitant a raison d'attendre ;
//   - « la plateforme ne l'expose pas » est un fait de the device : aucun
//     developpement n'y changera rien, et l'exploitant doit changer de hardware
//     ou renoncer.
//
// Les confondre costs du temps et de la credibilite. Un manque de NOTRE cote n'a
// donc rien a faire ici : il se voit a l'absence de collecte EN FACE d'un canal
// qui, lui, sait rendre la metrique (health, lldp et poe sont dans ce cas). Cette
// liste ne porte que ce que the device ne sert pas.
type MetricUnavailable struct {
	// Metric: named as in the standard model — the same key as on channels — so
	// « no channel renders this metric » and « here is why » answer under the same
	// name. Operator-facing copy is NOT here: the UI localizes from this key
	// (`monitoring.unavailable.<metric>`). The connector only declares absence.
	Metric string `json:"metric"`
	// Observed: is the absence ESTABLISHED on hardware, or only assumed from
	// product-line documentation?
	//
	// Same distinction as ResetProcedure.Confirmed, and for the same reason: verified
	// knowledge and assumed knowledge must not be shown the same way. An observed
	// absence is a fact shown without reservation; an assumed absence must stay
	// revisable, because later firmware or another model in the line may disprove it.
	Observed bool `json:"observed"`
	// Source: technical evidence — the reading that establishes absence when
	// Observed is true, the guide page that suggests it otherwise. For a skeptical
	// operator or engineer, not marketing copy (that lives in the UI catalog).
	Source string `json:"source,omitempty"`
}

// MonitoringCapabilities describes observability d'une plateforme.
//
// C'est le connecteur qui decide, pas un reglage : personne ne devrait avoir a
// savoir, device par device, ce que son parc supporte. Ce que la
// plateforme ne sait pas faire n'est pas contourne — the device apparait avec
// le canal dont il dispose, et l'UI dit lequel. Un parc a half supervise sans
// que personne ne le voie serait le pire des results.
//
// Voir docs/01-Architecture/Monitoring unifie et pipelines §1.
type MonitoringCapabilities struct {
	Channels []MonitorChannel `json:"channels"`
	// Traps / Syslog : the device sait-il POUSSER vers un collecteur ? C'est ce
	// qui donne la reactivity, there where le polling donne la reference.
	Traps  bool `json:"traps"`
	Syslog bool `json:"syslog"`
	// Streaming : gNMI ou equivalent.
	Streaming bool `json:"streaming"`
	// Unavailable : ce que la plateforme N'EXPOSE PAS, et pourquoi.
	//
	// Tout le reste de cette structure describes ce qui EXISTE ; sans ce champ,
	// l'absence n'est que du silence, et un silence ne se distingue pas d'un
	// oubli. C'est le pendant en lecture de instanton.WritableExtras, dont le vide
	// « se lit a the UI au lieu de se decouvrir sur le terrain ».
	//
	// Vide n'est PAS un aveu d'ignorance : la rule du projet reste « une
	// capability inconnue est supposee absente », et une metrique sans canal n'est
	// pas supervisee. Ce champ ne sert only ajouter le MOTIF there where on le connait.
	Unavailable []MetricUnavailable `json:"unavailable,omitempty"`
}

// Best rend le canal le plus rapide disponible pour une metrique data, et dit
// s'il en existe un. Sert a planifier : inutile de demander un rythme que
// the device ne tiendra pas.
func (m MonitoringCapabilities) Best(metric string) (MonitorChannel, bool) {
	var out MonitorChannel
	var found bool
	var c MonitorChannel
	for _, c = range m.Channels {
		var s string
		for _, s = range c.Metrics {
			if s != metric {
				continue
			}
			if !found || c.MinInterval < out.MinInterval {
				out, found = c, true
			}
		}
	}
	return out, found
}

// BestOfKind rend le canal le plus rapide d'un GENRE donne (`snmp`, `web`,
// `api`, `streaming`), et dit s'il en existe un.
//
// # Pourquoi Best ne suffisait pas, et ce que ca coutait
//
// Best rend le canal le plus rapide TOUS GENRES CONFONDUS, ce qui est la bonne
// response a « a quelle vitesse cette plateforme peut-elle be observee ». Ce
// n'est PAS la bonne response a « a quelle vitesse ai-je le droit d'interroger ce
// canal-ci », et c'est cette seconde question que la planification pose
// actually — parce que le collecteur periodique du produit s'execute sur le
// proxy et ne parle QUE SNMP (`internal/snmppoll`).
//
// Le default que cela produisait est verifiable : le connecteur RouterOS declares un
// canal `web` a 5 s et un canal `snmp` a 60 s, ce dernier deliberately haut parce
// qu'aucun OID n'y a jamais been lu. Best rendait donc 5 s, et le controleur
// poussait au proxy une cadence de 5 s — appliquee a un POLL SNMP, i.e. au
// canal dont le connecteur venait to write « jamais plus souvent que 60 s ». Le
// plancher d'un canal was oppose a un autre canal, ce qui est exactement ce qu'un
// plancher n'est pas.
//
// Le repli sur Best est laisse a l'APPELANT et n'est pas fait ici : une plateforme
// qui ne declares aucun canal du genre requested doit be traitee explicitement — soit
// en retombant sur le plancher de l'inconnu, soit en ne collectant pas — et masquer
// ce choix derriere un repli silencieux ferait interroger en SNMP une plateforme qui
// n'en a jamais annonce la capability.
func (m MonitoringCapabilities) BestOfKind(kind, metric string) (MonitorChannel, bool) {
	var out MonitorChannel
	var found bool
	var c MonitorChannel
	for _, c = range m.Channels {
		if c.Kind != kind {
			continue
		}
		var s string
		for _, s = range c.Metrics {
			if s != metric {
				continue
			}
			if !found || c.MinInterval < out.MinInterval {
				out, found = c, true
			}
		}
	}
	return out, found
}

// WhyUnavailable rend le motif declared pour une metrique que la plateforme
// n'expose pas. Interrogee avec la same key que Best : there where Best repond
// « aucun canal », celle-ci repond « et voici pourquoi il n'y en aura pas ».
//
// ok=false ne veut PAS dire que la metrique est disponible — il veut dire que
// personne n'a documente son absence. C'est Best qui dit la disponibilite.
func (m MonitoringCapabilities) WhyUnavailable(metric string) (MetricUnavailable, bool) {
	var u MetricUnavailable
	for _, u = range m.Unavailable {
		if u.Metric == metric {
			return u, true
		}
	}
	return MetricUnavailable{}, false
}

// Contradictions rend les metriques declarees A LA FOIS rendues par un canal et
// indisponibles sur la plateforme.
//
// Une declaration qui se contredit est pire que pas de declaration : soit the UI
// annonce une courbe qu'aucune collecte ne remplira jamais, soit il affiche un
// motif d'absence a cote d'une courbe qui existe. Dans les deux cas l'exploitant
// perd la seule chose que ces capabilities lui apportent — la certitude que ce
// qui est affiche correspond a the device.
//
// Ce n'est pas un control d'execution : c'est un invariant que les tests de
// chaque connecteur verrouillent, y compris ceux a venir. Il rend l'incoherence
// impossible a introduire en silence, dans un sens comme dans l'autre.
func (m MonitoringCapabilities) Contradictions() []string {
	var out []string
	var u MetricUnavailable
	for _, u = range m.Unavailable {
		var _, servi = m.Best(u.Metric)
		if servi {
			out = append(out, u.Metric)
		}
	}
	sort.Strings(out)
	return out
}

// EffectiveUsername resout le nom actually posable. Le nom souhaite n'est
// qu'un souhait : c'est ce que renvoie cette fonction qui atterrit sur
// the device, et donc ce qu'il faut enregistrer.
func (a AdminAccount) EffectiveUsername(wanted string) string {
	if a.Custom && wanted != "" {
		return wanted
	}
	if a.FixedName != "" {
		return a.FixedName
	}
	// Capability inconnue : on suppose le pire, i.e. le compte
	// historique de tout le secteur.
	return "admin"
}

// CommitStyle : comment la plateforme rend un changement durable. C'est ce qui
// determine la strategie de security de l'apply (cf. [[Reconciliation]]).
type CommitStyle string

const (
	// CommitImmediate : le changement s'applique et persiste d'un bloc. Le pire
	// cas — aucun filet fourni par la plateforme.
	CommitImmediate CommitStyle = "immediate"
	// CommitApplyThenSave : le changement s'applique tout de suite mais ne
	// survit pas a un reboot tant qu'il n'est pas explicitement sauve.
	// C'est un rollback gratuit : appliquer, verify, PUIS sauver.
	CommitApplyThenSave CommitStyle = "apply_then_save"
	// CommitConfirmed : la plateforme annule elle-same si on ne confirme pas.
	CommitConfirmed CommitStyle = "commit_confirmed"
	// CommitArchiveRollback : snapshot + restauration (IOS archive).
	CommitArchiveRollback CommitStyle = "archive_rollback"
)

// Capabilities : ce que la plateforme sait faire. Un intent qui demande autre
// chose est refused a la compilation, avec un message lisible — jamais un failure
// silencieux au deploiement.
type Capabilities struct {
	// VlanRange et MaxVlans : 802.1Q.
	VlanRange [2]int `json:"vlan_range"`
	MaxVlans  int    `json:"max_vlans"`
	// Features : ce qui est present ou absent, named comme dans le model
	// standard.
	Features map[string]bool `json:"features"`
	// STPModes : none | stp | rstp | mstp. "none" means the platform can turn
	// spanning tree off (RouterOS protocol-mode=none, Instant On admin mode 0).
	// Platforms that only speak one protocol (SwOS RSTP) omit "none" and list
	// that protocol alone — ResolveSTPMode will not honor a "none" preference
	// against them.
	STPModes []string `json:"stp_modes"`
	// MSTInstances, MaxACLs, MaxACEs, MaxStaticRoutes : les limites chiffrees,
	// qui servent a refuser AVANT to write.
	MSTInstances    int `json:"mst_instances"`
	MaxACLs         int `json:"max_acls"`
	MaxACEs         int `json:"max_aces"`
	MaxStaticRoutes int `json:"max_static_routes"`
	// MaxLAGs / PortsPerLAG : aggregation. Depend du nombre de ports du model.
	MaxLAGs     int `json:"max_lags"`
	PortsPerLAG int `json:"ports_per_lag"`
	// SNMPVersions : v1 | v2c | v3. Decide de identity de service poussee
	// (cf. docs/03-Securite/Identite de service et rotation).
	SNMPVersions []string `json:"snmp_versions"`
	// SNMPv3Auth / SNMPv3Priv : les algorithmes ACTUALLY disponibles.
	//
	// « Supporte v3 » n'est pas un booleen. Une plateforme peut annoncer v3 en
	// n'offrant que MD5 et DES, ce qui ne vaut pas mieux que du v2c — et la
	// present  comme sure serait le pire des mensonges. Le connecteur declares
	// donc les algorithmes, la politique porte un plancher, et l'ecart est
	// AFFICHE (spec §2.1 d'Identite de service et rotation).
	//
	// Ces listes decident aussi de l'ETIQUETTE que the device recevra
	// (`snmpv3-sha-des`), et donc du secret qu'il partagera : deux devices
	// aux algorithmes differents ne peuvent pas porter la same key v3.
	SNMPv3Auth []string `json:"snmpv3_auth,omitempty"`
	SNMPv3Priv []string `json:"snmpv3_priv,omitempty"`
	// Monitoring : par quels canaux cet device est supervisable, et a quelle
	// cadence c'est raisonnable.
	Monitoring MonitoringCapabilities `json:"monitoring"`
	// MaxSessions : sessions d'administration simultanees. Un connecteur qui
	// l'ignore fait tomber l'UI de the device sous ses propres requests.
	MaxSessions int `json:"max_sessions"`
	// Commit : la strategie de security de l'apply.
	Commit CommitStyle      `json:"commit"`
	Mgmt   MgmtCapabilities `json:"mgmt"`
	// Admin : ce que la plateforme accepte comme compte d'administration.
	Admin AdminAccount `json:"admin"`
}

// Has repond sans paniquer sur une carte nulle. Une feature inconnue est
// ABSENTE : pour tout ce qui peut couper access, l'inconnu se traite comme un
// refus.
func (c Capabilities) Has(feature string) bool {
	if c.Features == nil {
		return false
	}
	return c.Features[feature]
}

// ResetProcedure est du SAVOIR, pas une action : le connecteur describes la remise
// a zero, meshcraft ne l'execute pas a la place de the operator
// (cf. docs/01-Architecture/Discovery).
type ResetProcedure struct {
	// Hardware : le geste physique, describes sans ambiguite.
	Hardware string `json:"hardware,omitempty"`
	// Software : la voie logicielle quand access admin est conserve.
	Software string `json:"software,omitempty"`
	// Erases : ce qui est actually efface.
	Erases []string `json:"erases,omitempty"`
	// AfterReset : state dans lequel on retrouve the device — c'est ce qui
	// permet d'enchainer sur l'adoption.
	AfterReset []string `json:"after_reset,omitempty"`
	// Pitfalls : les pieges connus.
	Pitfalls []string `json:"pitfalls,omitempty"`
	// Confirmed : la procedure est-elle verified pour ce model, ou deduced
	// d'une gamme ? L'UI doit le dire — une procedure de reset erronee costs au
	// mieux du temps, au pire access a the device.
	Confirmed bool   `json:"confirmed"`
	Source    string `json:"source,omitempty"`
}

// Driver est le contrat d'un connecteur. Les methods non encore livrees
// renvoient ErrNotImplemented.
type Driver interface {
	Info() Info
	// Capabilities depend du model precise : un 8 ports et un 48 ports de la
	// same gamme n'ont pas les memes limites.
	Capabilities(model string) Capabilities
	// Fingerprint : « cet device est-il pour moi ? », de 0 a 100.
	Fingerprint(ev Evidence) int
	ResetProcedure(model string) ResetProcedure
}

// Evidence rassemble ce que la discovery a appris d'une adresse, sans prejuger
// du transport.
type Evidence struct {
	HTTPServer  string
	HTTPTitle   string
	HTTPBody    string
	SSHBanner   string
	ShellOut    string
	SysDescr    string
	SysObjectID string
	OpenPorts   []int
}

// --- Registre ---

var (
	registryMu sync.RWMutex
	registry   = map[string]Driver{}
)

// Register enregistre un connecteur. Appele depuis l'init des paquets de
// connecteurs.
func Register(d Driver) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[d.Info().Name] = d
}

// Get renvoie un connecteur par nom.
func Get(name string) (Driver, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	var d, ok = registry[name]
	return d, ok
}

// DriverNameFromExtra reads devices.extra.driver without failing when Extra also
// carries nested objects (stp_observed, …). Unmarshalling Extra into
// map[string]string rejects those shapes and used to make the connector look
// unknown (addressing apply shown as ANNOTATION only).
func DriverNameFromExtra(raw json.RawMessage) string {
	return ExtraString(raw, "driver")
}

// ExtraString reads a string field from devices.extra. Nested objects under other
// keys (stp_observed, …) are ignored — they must not make the whole decode fail.
func ExtraString(raw json.RawMessage, key string) string {
	if len(raw) == 0 || key == "" {
		return ""
	}
	var bag map[string]json.RawMessage
	if json.Unmarshal(raw, &bag) != nil {
		return ""
	}
	var v, ok = bag[key]
	if !ok || len(v) == 0 || string(v) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return ""
	}
	return s
}

// List renvoie tous les connecteurs, par nom.
func List() []Driver {
	registryMu.RLock()
	defer registryMu.RUnlock()
	var out = make([]Driver, 0, len(registry))
	var d Driver
	for _, d = range registry {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Info().Name < out[j].Info().Name })
	return out
}

// Best choisit le connecteur au meilleur score au-dessus du seuil. Renvoie
// ok=false si rien n'accroche — the device apparait alors comme non identifie
// rather que range de force dans une gamme.
func Best(ev Evidence, threshold int) (Driver, int, bool) {
	var best Driver
	var bestScore int
	var d Driver
	for _, d = range List() {
		var score = d.Fingerprint(ev)
		if score > bestScore {
			best, bestScore = d, score
		}
	}
	if best == nil || bestScore < threshold {
		return nil, bestScore, false
	}
	return best, bestScore, true
}

// Unused garde le contexte dans la signature du contrat a venir (ReadState,
// Plan, Apply) sans imposer de dependance from maintenant.
var _ = context.Background

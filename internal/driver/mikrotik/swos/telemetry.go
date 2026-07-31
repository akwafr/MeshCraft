package swos

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/metricbuf"
)

// La TELEMETRY d'un SwOS : ce que le scraping de pages sait rendre, et ce qu'il est
// le SEUL a savoir rendre sur cette plateforme.
//
// # Le scraping n'est plus le seul canal, et il reste le seul a tout rendre
//
// Ce fichier a longtemps commence par « pourquoi le scraping, et PAS SNMP », et la
// raison was dans le contrat : `SNMPVersions` was vide, donc `snmpprov.For`
// concluait « SNMP non managed », donc `snmpWatchesFor` ne poussait aucune cible au
// proxy. La page /snmp.b prouvait qu'un agent existait et was actif, sans dire a
// quelle VERSION il repondait — et aucune request ne lui avait jamais been adressee.
//
// Le walk du 29 juillet 2026 sur 10.99.0.36 a repondu. Le connecteur declares
// now `v2c` et un canal `snmp` (cf. swos.go, et le vault
// [[Telemetrie MikroTik]] § *Le SNMP du CSS326 : le verrou, et le observed qui l'a
// leve*). `snmpprov.For` conclut « v2c, NON conforme au plancher de la politique »,
// ce qui est la verite a afficher : supervise, et en clair.
//
// Ce qui suit garde donc tout son sens, mais pour une autre raison qu'avant : le
// scraping n'est plus un pis-aller, c'est le canal qui rend ce que l'agent NE rend
// pas. Deux grandeurs n'existent que dans ces pages — les PUISSANCES optiques
// (`tpw`, `rpw`), la table optique de l'agent s'arretant au courant de polarisation,
// et les RATES snapshots computed par the device (`rrb`, `trb`), que SNMP
// n'expose sous aucun OID. Aucun des deux canaux ne subsume l'autre.
//
// # Ce que les quatre pages rendent, et ce que ca COSTS
//
//	/link.b   etat de lien, activation, duplex, rate negocie, noms de ports  2 026 o
//	/stats.b  octets, paquets, RATES computed par the device, erreurs     12 694 o
//	/sys.b    temperature de puce, temps de fonctionnement                      780 o
//	/sfp.b    diagnostic optique complet des cages                             ~350 o
//
// DEUX requests HTTP par page, toujours : SwOS n'a pas de session, le Digest se
// renegocie a chaque fois. Une lecture de telemetry complete costs donc HUIT
// allers-retours, et c'est ce qui fonde les planchers declares dans swos.go — 10 s
// pour state, 15 s pour les compteurs, 30 s pour la sante et l'optique.
//
// # Ce que cette plateforme donne MIEUX que ses voisines
//
// Deux choses, et elles ne sont pas anecdotiques :
//
//   - LE RATE SNAPSHOT, calcule par the device (`rrb`, `trb`). meshcraft n'a donc
//     pas besoin de deriver deux lectures de compteur pour tracer une courbe, ce qui
//     est precisely ce qui rend un plancher de 15 s acceptable there where il ne le serait
//     pas ailleurs ;
//   - LE DIAGNOSTIC OPTIQUE, servi par une page de LECTURE ordinaire. Sur RouterOS il
//     faut une commande (`monitor`, donc l'API binaire ou un POST) ; ici c'est un GET.
//
// # Ce que cette plateforme ne donne PAS, et qui ne se contourne pas
//
// La liste est courte et elle est ESTABLISHED par enumeration de l'embedded interface,
// pas supposee : ni CPU, ni memory, ni LLDP, ni PoE sur ce model, ni horloge — donc
// aucun horodatage venant de the device. Les deux premieres manquent au point qu'il
// faut le dire a the UI : un CSS326 n'a AUCUNE serial de charge processeur, et ce n'est
// pas un lot de collecte qui manque, c'est le firmware qui ne publie pas la valeur.

// PortTelemetry est ce que les pages disent d'un port.
//
// Les champs numeriques sont des POINTEURS pour ce qui peut manquer, exactement comme
// dans le connecteur RouterOS voisin, et pour la same raison : sur un compteur, `0` et
// « pas lu » se ressemblent et se lisent a l'oppose. Le rate en est l'exemple qui
// compte — SwOS sert l'index 7, hors table, pour les ports SANS lien, et rendre « 10M »
// pour ces ports-there serait inventer un lien qui n'existe pas.
type PortTelemetry struct {
	Name string `json:"name"`
	// DeviceName est le nom que l'DEVICE donne au port (/link.b `nm`), rendu pour
	// le diagnostic et jamais employed comme clef de serial. Voir [foldLinkTelemetry].
	DeviceName string `json:"device_name,omitempty"`
	// Enabled : le port est-il administrativement actif (/link.b `en`).
	Enabled bool `json:"enabled"`
	// LinkUp : le port porte-t-il un lien (/link.b `lnk`).
	LinkUp bool `json:"link_up"`
	// FullDuplex : nil quand le port n'a pas de lien — le masque `dpx` n'a alors pas
	// de sens.
	FullDuplex *bool `json:"full_duplex,omitempty"`
	// SpeedMbps : le rate NEGOCIE. Nil sur un port sans lien, cf. le commentaire du
	// type et celui de [FieldSpeed].
	SpeedMbps *float64 `json:"speed_mbps,omitempty"`

	InOctets   *float64 `json:"in_octets,omitempty"`
	OutOctets  *float64 `json:"out_octets,omitempty"`
	InPackets  *float64 `json:"in_packets,omitempty"`
	OutPackets *float64 `json:"out_packets,omitempty"`
	// InBitsPerSecond / OutBitsPerSecond : les rates que l'DEVICE calcule.
	InBitsPerSecond  *float64 `json:"in_bits_per_second,omitempty"`
	OutBitsPerSecond *float64 `json:"out_bits_per_second,omitempty"`
	// InErrors : le compteur d'erreurs de receive du MAC (/stats.b `rte`).
	InErrors *float64 `json:"in_errors,omitempty"`
	// FCSErrors : les erreurs de sequence de control (/stats.b `rfcs`).
	//
	// PORTE ICI ET PAS ADDED A `InErrors`, et c'est une decision de prudence qu'il
	// faut expliquer parce qu'elle prive the UI de la mesure la plus parlante d'un
	// cable abime. Le observed de pages.go dit que `rte` porte « les erreurs de MAC en
	// receive » : si c'est un TOTAL, les erreurs de sequence de control en font
	// partie, et les ajouter compterait certaines trames deux fois. Une courbe d'erreurs
	// exageree est pire qu'une courbe absente — elle fait remplacer une jarretiere
	// saine. Le compteur reste donc visible au diagnostic, sans entrer dans la serial,
	// jusqu'a ce qu'une injection de trafic ait tranche lequel des deux contient l'autre.
	FCSErrors *float64 `json:"fcs_errors,omitempty"`
}

// OpticTelemetry est le diagnostic d'une cage SFP, tel que /sfp.b le rend.
type OpticTelemetry struct {
	// Port est le nom de front panel de la cage, pas son rang dans /sfp.b.
	//
	// La distinction est essentielle : les tableaux de /sfp.b sont indexes par CAGE
	// (deux entries sur un CSS326-24G-2S+), pas par port. La first cage est le port
	// d'index `sfpo` — 24, donc le port 25. Confondre les deux indexations
	// attribuerait le diagnostic optique de SFP1 au port 1, i.e. a un port
	// cuivre, et the UI afficherait une puissance optique sur une prise RJ45.
	Port string `json:"port"`
	// Present : la cage porte-t-elle un module. Reconnu a la sentinelle de temperature,
	// que the device rend a -128 °C sur une cage vide.
	Present      bool     `json:"present"`
	Vendor       string   `json:"vendor,omitempty"`
	Part         string   `json:"part,omitempty"`
	Serial       string   `json:"serial,omitempty"`
	TemperatureC *float64 `json:"temperature_c,omitempty"`
	SupplyVolts  *float64 `json:"supply_volts,omitempty"`
	TxBiasMA     *float64 `json:"tx_bias_ma,omitempty"`
	TxPowerDBM   *float64 `json:"tx_power_dbm,omitempty"`
	RxPowerDBM   *float64 `json:"rx_power_dbm,omitempty"`
	WavelengthNM *float64 `json:"wavelength_nm,omitempty"`
}

// Telemetry est the snapshot complet d'un SwOS.
type Telemetry struct {
	At time.Time `json:"at"`
	// TemperatureC : la temperature de puce (/sys.b `temp`), en degrees entiers.
	TemperatureC *float64 `json:"temperature_c,omitempty"`
	// UptimeSeconds : /sys.b `upt`, qui est en HUNDREDTHS de seconde.
	UptimeSeconds *float64 `json:"uptime_seconds,omitempty"`
	// Sensors : les capteurs OPTIONNELS de la carte — ventilateurs, tensions et
	// courants d'alimentation. Vides sur ce model, cf. [foldSysSensors].
	Sensors []Sensor `json:"sensors,omitempty"`

	Ports  []PortTelemetry  `json:"ports,omitempty"`
	Optics []OpticTelemetry `json:"optics,omitempty"`

	// Notes dit ce que cet snapshot ne contient PAS et pourquoi. Des phrases, comme
	// dans state : l'appelant les affiche et n'en tire aucune decision.
	Notes []string `json:"notes,omitempty"`
}

// Sensor est un capteur named de la carte, avec son unite.
//
// La forme est celle du connecteur RouterOS voisin (`name` / `type` / `value`), et ce
// n'est pas du mimetisme : c'est ce qui permet a un UI d'afficher la sante de deux
// gammes du same constructeur sans les distinguer. SwOS n'a pas de table de capteurs
// — ses champs sont fixes dans /sys.b — donc c'est ce connecteur qui NOMME, et il le
// fait avec les noms des champs de la page pour qu'un diagnostic remonte jusqu'a la
// source.
type Sensor struct {
	Name  string  `json:"name"`
	Type  string  `json:"type"`
	Value float64 `json:"value"`
}

// ReadTelemetry lit les quatre pages et rend the snapshot. LECTURE SEULE.
//
// HUIT ALLERS-RETOURS HTTP, et c'est le cost reel : deux par page, le Digest se
// renegociant a chaque fois. Les pages sont lues dans l'ordre du moins cher au plus
// cher, ce qui n'accelere rien mais rend un failure plus lisible — si /stats.b est la
// seule a manquer, c'est la page la plus lourde qui a expire, et le diagnostic n'est
// pas le same que si tout fails.
//
// FAILURE N'INTERROMPT PAS TOUT : une temperature de carte ne depend pas des
// compteurs. Seul /link.b est bloquant, et pour une raison de justesse et non de
// commodite — c'est lui qui porte la GEOMETRY du model (`prt`, `sfp`, `sfpo`). Sans
// elle, on ne sait ni combien de ports lire dans les tableaux de /stats.b, ni a quel
// port de front panel rattacher la first cage optique. Deviner la geometry
// attribuerait des compteurs au mauvais port, ce qui est pire qu'un trou.
func (a *Admin) ReadTelemetry(ctx context.Context) (Telemetry, error) {
	var out = Telemetry{At: time.Now()}
	var failures []string

	var link, err = a.Get(ctx, PageLink)
	if err != nil {
		return out, err
	}
	var ports, sfp, sfpFirst int
	ports, sfp, sfpFirst, err = geometryOf(link)
	if err != nil {
		a.step(false, "geometry du model illisible", err.Error())
		return out, err
	}
	// La geometry est retenue sur access comme le fait readAll : c'est elle qui NOMME
	// les prises, et les series d'un same device doivent porter les memes noms que
	// son etat lu — sans quoi un UI de diagnostic afficherait une courbe pour « SFP1 »
	// a cote d'une configuration pour un port qu'il ne saurait pas y rattacher.
	a.ports, a.sfp, a.sfpFirst = ports, sfp, sfpFirst
	var facade = a.facade()
	out.Ports = foldLinkTelemetry(link, facade)

	var sys *Page
	sys, err = a.Get(ctx, PageSys)
	if err != nil {
		failures = append(failures, "sante (/sys.b : "+err.Error()+
			") — ni temperature de puce, ni temps de fonctionnement")
	} else {
		foldSys(sys, &out)
	}

	var stats *Page
	stats, err = a.Get(ctx, PageStats)
	if err != nil {
		failures = append(failures, "compteurs (/stats.b : "+err.Error()+
			") — ni octets, ni paquets, ni rates, ni erreurs")
	} else {
		foldStats(stats, out.Ports)
	}

	// La page optique n'est lue que s'il y a des cages. Sur un model sans SFP, la
	// demander couterait deux requests pour recevoir l'interface web complete — un
	// chemin inconnu de SwOS rend 44 807 octets de HTML en HTTP 200, jamais un 404.
	if sfp > 0 {
		var optics *Page
		optics, err = a.Get(ctx, PageSFP)
		if err != nil {
			failures = append(failures, "diagnostic optique (/sfp.b : "+err.Error()+")")
		} else {
			out.Optics = foldOptics(optics, sfp, facade)
		}
	}

	if len(failures) > 0 {
		out.Notes = append(out.Notes, "LECTURE PARTIELLE — "+strconv.Itoa(len(failures))+
			" page(s) did not respond : "+strings.Join(failures, " ; ")+". Ce qui est "+
			"affiche est ce qui a pu be lu, et ne describes pas forcement the device.")
	}
	// Platform gaps (CPU, memory, …) are declared on Monitoring.Unavailable for the
	// model — the UI localizes from that capability. Snapshot notes do not duplicate
	// operator essays here.
	a.step(true, "telemetry lue",
		strconv.Itoa(len(out.Ports))+" port(s), "+strconv.Itoa(len(out.Optics))+" cage(s)")
	return out, nil
}

// --- Les traductions, toutes PURES et verified sur les captures reelles ---

// foldLinkTelemetry lit state des ports depuis /link.b.
//
// LE RATE N'EST RENDU QUE POUR LES PORTS AVEC LIEN, et c'est le trap de cette page.
// Releve sur l'reference device : les 25 ports sans lien portent l'index de
// rate 7, qui est HORS de la table `SpeedLabels` (sept entries, indices 0 a 6), et le
// seul port avec lien porte 3 — i.e. 10G, ce que confirme le transceiver
// insere. Un index hors table n'est donc pas « 10M par default », c'est une ABSENCE, et
// la rendre comme une valeur ferait afficher un rate sur vingt-cinq prises vides.
//
// Le duplex suit la same rule : le masque `dpx` n'a pas de sens sur un port sans
// lien, donc il n'est pas rendu rather que rendu faux — « demi-duplex » sur un port
// debranche est exactement le genre de valeur qui fait chercher un desaccord de
// negociation.
func foldLinkTelemetry(link *Page, f Facade) []PortTelemetry {
	var ports = f.Ports()
	var out = make([]PortTelemetry, 0, ports)
	var speeds, hasSpeeds = link.Nums(FieldSpeed)
	var names, hasNames = link.Texts(FieldPortName)

	var i int
	for i = 0; i < ports; i++ {
		var pt = PortTelemetry{Name: f.Name(i)}
		// Le nom OBSERVED dans `nm` est rendu a cote, jamais a la place — et ce n'est plus
		// « GE1 » que porte l'autre champ, mais « Port1 », i.e. la CONVENTION de
		// the device (cf. naming.go). Les deux se confondent donc sur un switch qu'on
		// n'a pas renomme, et c'est le but ; ils divergent from qu'un exploitant renomme une
		// prise, et c'est precisely pourquoi `nm` n'est pas la clef : c'est du texte
		// libre, il collisionne, et il change sans que rien ne le suive — en faire la clef
		// d'une serial ferait deux courbes d'un seul port au premier renommage. Il sert au
		// DIAGNOSTIC — reconnaitre a the UI la prise dont l'exploitant parle — et c'est
		// tout.
		if hasNames && i < len(names) {
			pt.DeviceName = strings.TrimSpace(names[i])
		}

		var up, okLink = link.Bit(FieldLink, i)
		pt.LinkUp = okLink && up
		var en, okEn = link.Bit(FieldEnabled, i)
		pt.Enabled = okEn && en

		if pt.LinkUp {
			var dpx, okDpx = link.Bit(FieldDuplex, i)
			if okDpx {
				var v = dpx
				pt.FullDuplex = &v
			}
			if hasSpeeds && i < len(speeds) {
				var label, okSpeed = SpeedLabel(speeds[i])
				if okSpeed {
					var mbps, okMbps = speedMbps(label)
					if okMbps {
						pt.SpeedMbps = &mbps
					}
				}
			}
		}
		out = append(out, pt)
	}
	return out
}

// speedMbps traduit un label de la table de the device en megabits par seconde.
//
// La table de SwOS est `"10M 100M 1G 10G 5G 2.5G 40G"`, et son ordre n'est PAS
// croissant — c'est already dit dans pages.go, et c'est la raison pour laquelle on
// traduit le LABEL et non l'indice. Traduire l'indice par une table parallele
// obligerait a tenir deux listes dans le same ordre, ce qui tient jusqu'au firmware
// qui en ajoute une huitieme au milieu.
func speedMbps(label string) (float64, bool) {
	switch label {
	case "10M":
		return 10, true
	case "100M":
		return 100, true
	case "1G":
		return 1000, true
	case "2.5G":
		return 2500, true
	case "5G":
		return 5000, true
	case "10G":
		return 10000, true
	case "40G":
		return 40000, true
	}
	return 0, false
}

// foldSys lit la sante depuis /sys.b.
//
// La temperature est en degrees Celsius ENTIERS, sans facteur d'echelle — observed a
// 0x44 puis 0x45 (68 puis 69 °C) a quelques minutes d'intervalle sur the device de
// reference, ce qui prouve un capteur vivant et non une constante de firmware.
//
// L'uptime est en HUNDREDTHS de seconde (`scale:100` dans l'interface). Releve
// 0x00e4cf41 = 14 995 265, soit 149 952 s — ce qui recoupe les deux valeurs notees
// pendant la campagne d'exploration (149 281 puis 149 801 secondes). Lire ce champ
// comme des secondes donnerait un temps de fonctionnement CENT FOIS trop grand, donc
// un device debout depuis cinq mois there where il l'est depuis deux jours.
func foldSys(sys *Page, out *Telemetry) {
	var temp, ok = sys.Num(FieldTemp)
	if ok {
		var v = float64(temp)
		out.TemperatureC = &v
	}
	var upt bool
	var raw uint32
	raw, upt = sys.Num(FieldUptime)
	if upt {
		var v = float64(raw) / 100
		out.UptimeSeconds = &v
	}
	out.Sensors = foldSysSensors(sys)
}

// Les champs de capteurs OPTIONNELS de /sys.b, observed a ZERO sur le model de
// reference.
//
// Ils sont named ici rather que dans pages.go parce qu'ils ne servent only la
// telemetry : pages.go documente ce que le connecteur LIT ou WRITES pour configurer, et
// aucun de ceux-ci n'entre dans une write.
var sysSensors = []struct {
	Field string
	Name  string
	Type  string
	Scale float64
}{
	{"fan1", "fan1", "RPM", 1},
	{"fan2", "fan2", "RPM", 1},
	{"fan3", "fan3", "RPM", 1},
	{"fan4", "fan4", "RPM", 1},
	// Les tensions et courants d'alimentation. Scale n'est PAS established — voir le
	// commentaire de foldSysSensors, qui explique pourquoi ces capteurs ne produisent
	// aucune serial sur cette gamme et pourquoi c'est sans consequence.
	{"p1v", "psu1-voltage", "V", 1},
	{"p2v", "psu2-voltage", "V", 1},
	{"btm1", "board1", "C", 1},
	{"btm2", "board2", "C", 1},
}

// foldSysSensors observes les capteurs optionnels, et n'en rend QUE les non nuls.
//
// # Pourquoi filtrer sur zero, alors que c'est une heuristique
//
// SwOS ne distingue pas « ce capteur n'existe pas » de « ce capteur vaut zero » : les
// champs sont TOUJOURS present dans la page, a zero quand la carte n'a pas
// l'electronique correspondante. Releve sur le CSS326-24G-2S+ : `fan1` a `fan4`, `p1v`,
// `p2v`, `btm1`, `btm2` valent tous 0x00000000, et l'interface de the device
// n'affiche aucun de ces champs.
//
// Publier ces zeros produirait, sur tout le parc SwOS sans ventilateur, quatre series
// « ventilateur a 0 tr/min » et deux « alimentation a 0 V » — i.e. une alerte
// permanente et fausse sur la majorite des devices. Les filtrer est donc le seul
// comportement tenable.
//
// # La limite que ce filtre creates, dite rather que masquee
//
// Un ventilateur ACTUALLY STOPPED vaut zero lui aussi, et devient donc indiscernable
// d'un ventilateur absent. Aucun code ne peut lever cette ambiguite : la page ne porte
// pas l'information. Ce qui la contient partiellement, et qui reste disponible, c'est
// la TEMPERATURE — une carte dont le ventilateur s'est stopped chauffe, et c'est ce
// signal-there qu'il faut surveiller sur cette plateforme, pas la vitesse du ventilateur.
//
// Scale des tensions n'est pas established non plus, et c'est sans consequence
// AUJOURD'HUI precisely a cause du filtre : aucun model observed ne sert de valeur non
// nulle, donc aucune serial de tension n'est jamais produite. Le jour where un chassis CRS
// sous SwOS en servirait une, the scale devra be established avant de croire le chiffre —
// c'est written ici pour que ce soit lu a ce moment-there.
func foldSysSensors(sys *Page) []Sensor {
	var out []Sensor
	var i int
	for i = range sysSensors {
		var raw, ok = sys.Num(sysSensors[i].Field)
		if !ok || raw == 0 {
			continue
		}
		out = append(out, Sensor{
			Name:  sysSensors[i].Name,
			Type:  sysSensors[i].Type,
			Value: float64(raw) * sysSensors[i].Scale,
		})
	}
	return out
}

// rateScale est le diviseur des champs de rate de /stats.b.
//
// 100, et cette valeur n'est pas seulement observed dans l'interface (`scale:.01`) :
// elle est CORROBORATED par les compteurs de la same capture, ce qui est plus solide
// qu'une lecture de code JavaScript. Le calcul, sur le seul port qui porte un lien
// (index 25, la cage SFP2) :
//
//	octets recus       rb  = 0x018f2a86 = 26 159 750
//	temps de marche    upt = 0x00e4cf41 = 14 995 265 hundredths = 149 952 s
//	rate MOYEN                          ≈ 1 396 bit/s
//	rate snapshot   rrb = 0x0000145f = 5 215 → /100 = 52,15 bit/s
//
// Snapshot est bien SOUS la moyenne, ce qui est consistent avec un lien de
// laboratoire calme after une campagne d'adoption. L'autre hypothese — un facteur
// ×100, donc 521 500 bit/s — impliquerait 9,8 Go transferes sur la duree de marche,
// contre 26 Mo actually comptes : elle est ecartee par trois ordres de grandeur.
//
// Ce qui n'est PAS established pour autant : que ce rate soit une moyenne sur la same
// fenetre que celle de l'interface. Aucune injection de trafic n'a been faite. Ce
// chiffre est donc bon a un facteur d'echelle pres et a une fenetre pres, et il ne faut
// pas batir un seuil d'alerte serre dessus avant de l'avoir confronte a un rate connu.
const rateScale = 100

// foldStats verse les compteurs de /stats.b dans la liste de ports.
//
// MODIFIE la tranche recue, rather que d'en rendre une seconde. C'est le seul endroit
// du fichier qui le fait, et la raison est la clef d'indexation : les tableaux de
// /stats.b sont indexes par RANG DE PORT, dans le same ordre que la geometry de
// /link.b. Rendre une seconde liste obligerait a recoller les deux par leur nom, donc a
// refabriquer une correspondance qui existe already par construction — et une
// correspondance refabriquee est une correspondance qui peut se tromper.
//
// LES COMPTEURS D'OCTETS SONT SUR 64 BITS, en deux halves : `rb` porte les 32 bits
// bas, `rbh` les 32 hauts. Lire `rb` seul ferait repasser le compteur a zero tous les
// 4 Gio — sur un lien 10G, toutes les trois secondes et demie, ce qui produirait une
// courbe de trafic entirely fausse. C'est [Counter64] qui les recompose, et ce n'est
// pas une precaution theorique : ce connecteur supervise des cages 10G.
func foldStats(stats *Page, ports []PortTelemetry) {
	var rb, hasRb = stats.Nums(FieldRxBytes)
	var rbh, _ = stats.Nums(FieldRxBytesHigh)
	var tb, hasTb = stats.Nums(FieldTxBytes)
	var tbh, _ = stats.Nums(FieldTxBytesHigh)
	var rtp, hasRtp = stats.Nums(FieldRxPackets)
	var ttp, hasTtp = stats.Nums(FieldTxPackets)
	var rrb, hasRrb = stats.Nums(FieldRxRate)
	var trb, hasTrb = stats.Nums(FieldTxRate)
	var rte, hasRte = stats.Nums(FieldRxErrors)
	var rfcs, hasRfcs = stats.Nums(FieldRxFCS)

	var i int
	for i = range ports {
		if hasRb {
			assign64(&ports[i].InOctets, rb, rbh, i)
		}
		if hasTb {
			assign64(&ports[i].OutOctets, tb, tbh, i)
		}
		if hasRtp {
			assignAt(&ports[i].InPackets, rtp, i, 1)
		}
		if hasTtp {
			assignAt(&ports[i].OutPackets, ttp, i, 1)
		}
		if hasRrb {
			assignAt(&ports[i].InBitsPerSecond, rrb, i, 1.0/rateScale)
		}
		if hasTrb {
			assignAt(&ports[i].OutBitsPerSecond, trb, i, 1.0/rateScale)
		}
		if hasRte {
			assignAt(&ports[i].InErrors, rte, i, 1)
		}
		if hasRfcs {
			assignAt(&ports[i].FCSErrors, rfcs, i, 1)
		}
	}
}

// assign64 recompose un compteur 64 bits depuis ses deux halves.
//
// Une half HAUTE plus courte que la basse n'est pas une erreur : elle est traitee
// comme un zero, ce qui est exactement ce qu'un firmware qui n'aurait pas encore
// depasse 4 Gio sur ce port rendrait. Refuser le compteur entier pour cette raison
// priverait de trafic un device parfaitement lisible.
func assign64(dst **float64, low, high []uint32, i int) {
	if i >= len(low) {
		return
	}
	var hi uint32
	if i < len(high) {
		hi = high[i]
	}
	var v = float64(Counter64(low[i], hi))
	*dst = &v
}

// assignAt pose la valeur d'un tableau a l'index donne, mise a the scale.
func assignAt(dst **float64, vals []uint32, i int, scale float64) {
	if i >= len(vals) {
		return
	}
	var v = float64(vals[i]) * scale
	*dst = &v
}

// foldOptics lit /sfp.b.
//
// # L'INDEXATION est le seul vrai trap de cette page
//
// Ses tableaux sont indexes par CAGE et non par port : deux entries sur un
// CSS326-24G-2S+, alors que /stats.b en porte vingt-six. La cage de rang `i` est le
// port d'index `sfpFirst + i` — observed `sfpo: 0x18`, donc la first cage est le port
// d'index 24, i.e. le port numerote 25. Recoller les deux indexations en
// supposant qu'elles coincident attribuerait le diagnostic optique de SFP1 au port 1,
// et the UI afficherait une puissance optique sur une prise cuivre.
//
// # Une cage vide se reconnait a sa TEMPERATURE
//
// -128 °C, i.e. 0xffffff80 en complement a deux, est le « pas de module » de
// l'interface (`u:-128`). Ce n'est PAS une temperature, et la traiter comme telle
// ferait apparaitre deux capteurs a -128 °C sur tout switch dont les cages sont libres.
// Releve sur l'reference device : `tmp:[0xffffff80,0x00000032]` — la cage 1 est
// vide, la cage 2 porte un module a 50 °C.
//
// # Les unites, toutes observed dans l'embedded interface
//
//	tmp  °C entiers                       0x32   = 50 °C
//	vcc  millivolts (scale:1E3)           0x0cbd = 3,261 V
//	tbs  milliamperes                     0x22   = 34 mA
//	wln  hundredths de nanometre (1E2)     0x0001f018 = 1 270 nm
//	tpw  dixiemes de microwatt (1E4, ma:1) 0x12b6 = 4 790 → −3,1966 dBm
//	rpw  idem                             0x0ad2 = 2 770 → −5,5752 dBm
//
// La longueur d'onde observed, 1 270 nm, recoupe le champ descriptif de la same page
// (`typ: '1270nm & smf'`) et la reference du module (`SFP-10G-BX`). Ce n'est donc pas
// une echelle supposee : deux champs independants de la same capture concordent.
func foldOptics(page *Page, sfp int, f Facade) []OpticTelemetry {
	var out = make([]OpticTelemetry, 0, sfp)
	var temps, _ = page.Nums(FieldSFPTemp)
	var vcc, _ = page.Nums(FieldSFPVcc)
	var bias, _ = page.Nums(FieldSFPTxBias)
	var txp, _ = page.Nums(FieldSFPTxPower)
	var rxp, _ = page.Nums(FieldSFPRxPower)
	var wln, _ = page.Nums("wln")
	var vendor, _ = page.Texts(FieldSFPVendor)
	var part, _ = page.Texts(FieldSFPPart)
	var serial, _ = page.Texts(FieldSFPSerial)

	var i int
	for i = 0; i < sfp; i++ {
		var o = OpticTelemetry{
			// La cage de rang `i` est le port d'index `sfpFirst+i`, et c'est la front panel qui
			// le NOMME : « SFP1 » sur l'reference device, there where la numerotation
			// continue disait « GE25 ». Le nom passe par le same chemin que celui de state
			// lu, sinon un diagnostic optique s'afficherait sur un port que the UI ne sait
			// pas retrouver.
			Port:   f.Name(f.SFPFirst() + i),
			Vendor: textAt(vendor, i),
			Part:   textAt(part, i),
			Serial: textAt(serial, i),
		}
		if i < len(temps) && temps[i] != sfpNoModule {
			var v = float64(int32(temps[i])) //nolint:gosec // complement a deux voulu, cf. ci-dessous
			o.TemperatureC = &v
			o.Present = true
		}
		if !o.Present {
			// Une cage sans module ne produit ni mesure ni identity : les champs texte
			// d'une cage vide sont des strings d'espaces (observed `vnd:['', …]`), et les
			// publier ferait un transceiver fantome dans l'inventaire.
			o.Vendor, o.Part, o.Serial = "", "", ""
			out = append(out, o)
			continue
		}
		assignAt(&o.SupplyVolts, vcc, i, 1.0/1000)
		assignAt(&o.TxBiasMA, bias, i, 1)
		assignAt(&o.WavelengthNM, wln, i, 1.0/100)
		assignDBM(&o.TxPowerDBM, txp, i)
		assignDBM(&o.RxPowerDBM, rxp, i)
		out = append(out, o)
	}
	return out
}

// textAt rend une ingress de tableau de texte, ROGNEE de son remplissage.
//
// Les champs identity d'un transceiver sont de taille fixe dans le SFF-8472 et
// rembourres par des ESPACES — observed `'46532020202020202020202020202020'`, soit « FS »
// suivi de quatorze espaces. Le rognage a lieu ICI, a l'affichage, et jamais a la
// lecture de la page : rogner en lisant modifierait la longueur d'un champ que
// writing doit rendre a l'identique.
func textAt(vals []string, i int) string {
	if i >= len(vals) {
		return ""
	}
	return strings.TrimSpace(vals[i])
}

// assignDBM convertit une puissance optique brute en dBm.
//
// # Unit, et pourquoi la conversion vit ICI et pas dans le vocabulaire de series
//
// Les trois canaux MikroTik servent la same grandeur physique dans TROIS echelles
// differentes, et c'est ce qui interdit une conversion unique :
//
//	SwOS            entier BRUT en dixiemes de microwatt → logarithme a faire
//	RouterOS API    valeur already en dBm, servie telle quelle par `monitor`
//	SNMP MikroTik   entier en THOUSANDTHS de dBm (mtxrOpticalTxPower)
//
// Le vocabulaire de series n'impose donc qu'une UNITE — le dBm, parce qu'un budget
// optique s'written en dB et que les seuils d'un constructeur de transceiver sont donnes
// ainsi — et laisse chaque canal faire son propre chemin pour y arriver.
//
// # Zero n'a pas de valeur en dBm
//
// log₁₀(0) n'existe pas. Une cage sous tension mais sans lumiere — fibre debranchee,
// device d'en face off — rend donc AUCUN sample rather que moins l'infini.
// Ce n'est pas une coquetterie mathematique : un point a -Inf dans VictoriaMetrics
// ecrase the scale du graphe entier, si bien qu'une seule fibre debranchee rendrait
// illisibles les courbes de tous les liens qui fonctionnent.
func assignDBM(dst **float64, vals []uint32, i int) {
	if i >= len(vals) || vals[i] == 0 {
		return
	}
	var v = 10 * math.Log10(float64(vals[i])/10000)
	*dst = &v
}

// --- Le passage au vocabulaire de series ---

// Samples traduit the snapshot dans le vocabulaire de series du projet.
//
// Fonction PURE. Comme celle du connecteur RouterOS, elle ne produit NI `ifindex` NI
// `iftype` : ce canal n'a pas d'index d'interface a donner, et fabriquer un index
// depuis un rang de port serait inventer une clef que rien ne garantit stable. La
// consequence est celle qu'on veut — ces samples alimentent les courbes sans
// jamais pouvoir modifier la front panel (« la telemetry ne CREATES jamais un port »).
//
// Le rattachement au port se fait donc par le NOM, et il tombe ici dans le cas le plus
// favorable : les noms sont ceux que meshcraft a COMPUTED depuis le prefix de la
// cible, donc ils coincident already avec la front panel qu'il tient en base. Cette plateforme
// numerote ses ports et ne les nomme pas — c'est justement pour cela que le prefix
// vient de la cible et non de l'appel (cf. [Admin.ReadState]).
func (t Telemetry) Samples(deviceID string) []metricbuf.Sample {
	var out []metricbuf.Sample
	var at = t.At
	if at.IsZero() {
		at = time.Now()
	}
	var base = map[string]string{"device": deviceID}
	var add = func(name string, v *float64, labels map[string]string) {
		if v == nil {
			return
		}
		out = append(out, metricbuf.Sample{Name: name, Labels: labels, Value: *v, TS: at})
	}

	add(metricbuf.MetricUptime, t.UptimeSeconds, base)
	if t.TemperatureC != nil {
		// Le capteur est NOMME, same s'il est seul sur cette carte. Une serial de
		// temperature sans tag `sensor` ne pourrait pas cohabiter avec celles des
		// gammes qui en ont plusieurs, et il faudrait alors deux requests differentes
		// pour tracer la same grandeur sur deux constructeurs.
		add(metricbuf.MetricTemperature, t.TemperatureC,
			map[string]string{"device": deviceID, "sensor": "chip"})
	}
	var s Sensor
	for _, s = range t.Sensors {
		var labels = map[string]string{"device": deviceID, "sensor": s.Name}
		var v = s.Value
		switch s.Type {
		case "C":
			add(metricbuf.MetricTemperature, &v, labels)
		case "V":
			add(metricbuf.MetricVoltage, &v, labels)
		case "RPM":
			add(metricbuf.MetricFanRPM, &v, labels)
		}
	}

	var p PortTelemetry
	for _, p = range t.Ports {
		var labels = map[string]string{"device": deviceID, "port": p.Name}
		var state float64
		if p.LinkUp {
			state = 1
		}
		out = append(out, metricbuf.Sample{
			Name: metricbuf.MetricIfOperStatus, Labels: labels, Value: state, TS: at,
		})
		add(metricbuf.MetricIfSpeedMbps, p.SpeedMbps, labels)
		add(metricbuf.MetricIfInOctets, p.InOctets, labels)
		add(metricbuf.MetricIfOutOctets, p.OutOctets, labels)
		add(metricbuf.MetricIfInPackets, p.InPackets, labels)
		add(metricbuf.MetricIfOutPackets, p.OutPackets, labels)
		add(metricbuf.MetricIfInBitsPerSecond, p.InBitsPerSecond, labels)
		add(metricbuf.MetricIfOutBitsPerSecond, p.OutBitsPerSecond, labels)
		add(metricbuf.MetricIfInErrors, p.InErrors, labels)
	}

	var o OpticTelemetry
	for _, o = range t.Optics {
		if !o.Present {
			continue // une cage vide ne produit rien, cf. foldOptics
		}
		var labels = map[string]string{"device": deviceID, "port": o.Port}
		add(metricbuf.MetricSFPTemperature, o.TemperatureC, labels)
		add(metricbuf.MetricSFPSupplyVolts, o.SupplyVolts, labels)
		add(metricbuf.MetricSFPTxBiasMilliamps, o.TxBiasMA, labels)
		add(metricbuf.MetricSFPTxPowerDBM, o.TxPowerDBM, labels)
		add(metricbuf.MetricSFPRxPowerDBM, o.RxPowerDBM, labels)
		add(metricbuf.MetricSFPWavelengthNM, o.WavelengthNM, labels)
	}
	return out
}

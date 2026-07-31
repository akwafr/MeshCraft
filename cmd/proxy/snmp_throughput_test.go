package main

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/snmppoll"
)

// Le reglage du rate de remontee, mis noir sur blanc.
//
// Ce fichier n'existe pas pour prouver que le code marche : il existe pour que le
// jour where quelqu'un retouche `flushEvery`, `maxPerFlush`, `maxFlushBurst` ou
// `bufCapacity`, il voie CE QU'IL CASSE. Chaque constante de ce lot a been choisie
// contre un chiffre, et un chiffre non verified redevient une opinion en six mois.

// Les OID des compteurs, en complement de ceux que trap_test.go declares already pour
// `if_state` (`tOidIfDescr`, `tOidIfType`, `tOidIfOperStatus`) — reused tels
// quels, le contrat avec `snmppoll` etant le same. Recopies parce que `snmppoll`
// les garde prives, et c'est sans risque : ce sont des constantes de PROTOCOLE.
const (
	cOidIfHighSpeed   = "1.3.6.1.2.1.31.1.1.1.15"
	cOidIfHCInOctets  = "1.3.6.1.2.1.31.1.1.1.6"
	cOidIfHCOutOctets = "1.3.6.1.2.1.31.1.1.1.10"
	cOidIfInErrors    = "1.3.6.1.2.1.2.2.1.14"
	cOidIfOutErrors   = "1.3.6.1.2.1.2.2.1.20"
)

// portsDu1930_48 : 48 ports cuivre + 4 emplacements SFP. C'est la front panel qui sert
// de reference a tout le calcul de rate, parce que c'est le plus gros device
// de la gamme que meshcraft pilote.
const portsDu1930_48 = 52

// Ce que le controleur accepte, en OCTETS et en NOMBRE. Les deux comptent, et c'est
// tout l'objet de la mise a jour : `maxPerFlush` a deux murs, pas un.
//
// Les valeurs sont celles de `maxProxyMetricsBody` et `maxSamplesPerBatch`
// (internal/api/proxymetrics_handlers.go). Recopiees et non importees : `cmd/proxy`
// est un binaire deploye a part, il ne DOIT rien devoir a la version du controleur
// qui l'ecoute — et c'est justement ce qu'on veut eprouver, la coincidence entre deux
// reglages qui vivent dans deux processus.
//
// Le chiffre previous was 1 MiB, celui du decoder generique de l'API
// (internal/api.decode) que cette route empruntait alors. La route a now son
// propre plafond, desserre a 8 MiB pour que son garde-fou affiche de 20 000
// samples devienne enfin atteignable. Les assertions written contre 1 MiB
// passaient toujours — elles were devenues conservatrices, pas fausses — mais un
// chiffre juste par accident redevient une opinion a la first relecture.
//
// Le depassement ne BLOQUE plus, et ca non plus n'est pas un detail : le controleur
// repond un 413 explicite, et `flush` abandonne le lot en le journalisant (cf.
// refusDefinitif) au lieu de le rejouer sans fin. Ce que ce test protege n'est donc
// plus un blocage mais un GASPILLAGE — un reglage qui ferait de l'abandon la routine.
const (
	controllerBodyCapBytes = 8 << 20
	controllerMaxSamples   = 20000
)

// refInstant : un horodatage avec ses NANOSECONDES, et ca compte. Un instant rond
// s'encode « 2026-07-28T12:00:00Z », dix octets de moins par sample qu'un
// instant reel. Mesurer un lot sur un instant rond le sous-estimerait de 5 % — sur
// une assertion dont tout l'objet est une marge, c'est l'erreur a ne pas faire.
func refInstant() time.Time {
	return time.Date(2026, 7, 28, 12, 0, 0, 123456789, time.UTC)
}

// tDeviceID / tProxyID / tSecret : des UUID et un secret de la longueur reelle. Un
// identifiant d'device se paie sur CHAQUE sample, mesurer un « sw1 » de test
// sous-estimerait le lot.
const (
	tDeviceID = "6f1c9a2e-4b7d-4c3a-9f88-2d0e5b71a3c4"
	tProxyID  = "b2f4d1c8-9e3a-4f52-8c11-7a6d0b4e2f39"
	tSecret   = "0123456789abcdef0123456789abcdef"
)

// tPortUsine : le nommage d'usine d'un Instant On 1930, tel qu'il sort de la boite.
func tPortUsine(i int) string { return "GE" + strconv.Itoa(i) }

// tPortRenomme : un port renomme par l'exploitant, 37 caracteres. C'est LUI le cas
// dimensionnant, et pas par pessimisme : le controleur ne borne pas la longueur de ce
// label, l'exploitant la choisit. C'est la same forme que celle mesuree cote
// controleur (worstBytesPerSample), pour que les deux paquets parlent du same
// device quand ils annoncent un poids par sample.
func tPortRenomme(i int) string {
	return "Salle serveurs - baie B12 - uplink " + strconv.Itoa(i)
}

func tPDU(oid, idx string, typ gosnmp.Asn1BER, v any) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid + "." + idx, Type: typ, Value: v}
}

// tablesCompteurs fabrique les tables qu'un walk `if_counters` rapporte d'un
// 1930 : compteurs a douze chiffres, pour que la taille mesuree soit celle d'un
// device en service et non celle d'un cas d'ecole. Le nommage des ports est
// parametre parce que c'est la SEULE variable de poids que le produit ne maitrise
// pas — voir tPortUsine et tPortRenomme.
func tablesCompteurs(nom func(int) string, n int) map[string]map[string]gosnmp.SnmpPDU {
	var tables = map[string]map[string]gosnmp.SnmpPDU{
		tOidIfDescr: {}, tOidIfType: {}, cOidIfHCInOctets: {},
		cOidIfHCOutOctets: {}, cOidIfInErrors: {}, cOidIfOutErrors: {},
	}
	var i int
	for i = 1; i <= n; i++ {
		var idx = strconv.Itoa(i)
		tables[tOidIfDescr][idx] = tPDU(tOidIfDescr, idx, gosnmp.OctetString,
			[]byte(nom(i)))
		tables[tOidIfType][idx] = tPDU(tOidIfType, idx, gosnmp.Integer,
			snmppoll.IfTypeEthernet)
		// Les magnitudes sont celles du jeu d'essai du controleur
		// (internal/api/proxymetrics_limits_test.go) : treize chiffres pour un compteur
		// d'octets, quatre pour un compteur d'erreurs. Ce n'est pas de la coquetterie —
		// le nombre de CHIFFRES compte dans le poids JSON, et deux paquets qui annoncent
		// « 196 o par sample » doivent avoir mesure le same device, sinon le
		// chiffre commun ne veut rien dire.
		tables[cOidIfHCInOctets][idx] = tPDU(cOidIfHCInOctets, idx, gosnmp.Counter64,
			uint64(4982133770615))
		tables[cOidIfHCOutOctets][idx] = tPDU(cOidIfHCOutOctets, idx, gosnmp.Counter64,
			uint64(3170488291204))
		tables[cOidIfInErrors][idx] = tPDU(cOidIfInErrors, idx, gosnmp.Counter32, uint(1734))
		tables[cOidIfOutErrors][idx] = tPDU(cOidIfOutErrors, idx, gosnmp.Counter32, uint(902))
	}
	return tables
}

func tablesEtat(nom func(int) string, n int) map[string]map[string]gosnmp.SnmpPDU {
	var tables = map[string]map[string]gosnmp.SnmpPDU{
		tOidIfDescr: {}, tOidIfType: {}, tOidIfOperStatus: {}, cOidIfHighSpeed: {},
	}
	var i int
	for i = 1; i <= n; i++ {
		var idx = strconv.Itoa(i)
		tables[tOidIfDescr][idx] = tPDU(tOidIfDescr, idx, gosnmp.OctetString,
			[]byte(nom(i)))
		tables[tOidIfType][idx] = tPDU(tOidIfType, idx, gosnmp.Integer,
			snmppoll.IfTypeEthernet)
		tables[tOidIfOperStatus][idx] = tPDU(tOidIfOperStatus, idx, gosnmp.Integer,
			ifOperUp)
		tables[cOidIfHighSpeed][idx] = tPDU(cOidIfHighSpeed, idx, gosnmp.Gauge32,
			uint(1000))
	}
	return tables
}

// TestProductionParEquipement fonde l'arithmetique du reglage sur le CODE et non
// sur un commentaire : quatre samples par port pour les compteurs, deux pour
// state. Si `snmppoll.Samples` en emet un cinquieme un jour, le rate soutenable
// annonce plus bas devient faux, et c'est ici qu'on l'apprend.
func TestProductionParEquipement(t *testing.T) {
	var at = refInstant()
	var counters = snmppoll.Samples(tDeviceID,
		tablesCompteurs(tPortUsine, portsDu1930_48), at, 0)
	if len(counters) != 4*portsDu1930_48 {
		t.Fatalf("if_counters : %d samples attendus (4 par port), obtenu %d",
			4*portsDu1930_48, len(counters))
	}
	var state = snmppoll.Samples(tDeviceID,
		tablesEtat(tPortUsine, portsDu1930_48), at, 0)
	if len(state) != 2*portsDu1930_48 {
		t.Fatalf("if_state : %d samples attendus (2 par port), obtenu %d",
			2*portsDu1930_48, len(state))
	}

	// La cadence decidee par l'exploitant : compteurs a 5 s, etat a 30 s. Les
	// valeurs sont celles du controleur (internal/api/watch_handlers.go) ; les
	// repeter ici est volontaire, le proxy ne les connait pas — c'est le controleur
	// qui les lui envoie, et ce test raisonne sur la consigne, pas sur un reglage
	// local.
	var parSeconde = float64(len(counters))/5 + float64(len(state))/30
	if parSeconde < 44 || parSeconde > 46 {
		t.Fatalf("~45 samples/s par device attendus, obtenu %.1f", parSeconde)
	}
	t.Logf("un 1930 48 ports produit %.1f samples/s "+
		"(%d compteurs / 5 s + %d etats / 30 s)",
		parSeconde, len(counters), len(state))
}

// corpsDuLot serialise l'enveloppe EXACTE que `flush` envoie : les deux champs
// d'authentification comptent dans les octets que le controleur borne.
func corpsDuLot(t *testing.T, lot []metricbuf.Sample) []byte {
	t.Helper()
	var body, err = json.Marshal(map[string]any{
		"proxy_id": tProxyID, "secret": tSecret, "samples": lot,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// etireLot repete un jeu d'samples jusqu'a `n`. Ce qu'on mesure est la FORME
// d'un sample ; sa repetition n'en change pas le poids.
func etireLot(proto []metricbuf.Sample, n int) []metricbuf.Sample {
	var out = make([]metricbuf.Sample, 0, n)
	var i int
	for i = 0; i < n; i++ {
		out = append(out, proto[i%len(proto)])
	}
	return out
}

// TestUnLotTientSousLePlafondDuControleur est l'assertion la plus importante du
// fichier, et la moins intuitive : le mur de `maxPerFlush` n'est pas dans ce paquet.
//
// Il est double — le corps en OCTETS et le lot en NOMBRE — et il a CHANGED DE COTE.
// Tant que la route empruntait le decoder generique a 1 MiB, c'were les octets
// qui mordaient les premiers : l'ancien reglage de 5000 samples pesait 1015 KiB,
// soit 99 % du plafond, donc un blocage a la first tag un peu plus longue.
// Depuis que la route a son propre plafond de 8 MiB, c'est le garde-fou en nombre
// (20 000) qui arrive le premier. Le test ne recopie donc plus « ~4500 » : il CALCULE
// le mur a partir du poids qu'il vient de mesurer, et il echouera si `maxPerFlush`
// s'en approche, quel que soit celui des deux murs qui bouge.
func TestUnLotTientSousLePlafondDuControleur(t *testing.T) {
	var at = refInstant()
	// Deux nommages, parce que le poids d'un sample n'a qu'une variable libre et
	// que c'est celle-there. Mesure ici rather que recopie d'un commentaire : les 208 o
	// qu'annoncait ce fichier venaient d'un prototype nommant ses ports
	// « GigabitEthernet52 », un nommage que personne n'a en production sur un 1930.
	var court = snmppoll.Samples(tDeviceID,
		tablesCompteurs(tPortUsine, portsDu1930_48), at, 0)
	var long = snmppoll.Samples(tDeviceID,
		tablesCompteurs(tPortRenomme, portsDu1930_48), at, 0)
	var poidsCourt = len(corpsDuLot(t, court)) / len(court)
	var poidsLong = len(corpsDuLot(t, long)) / len(long)
	t.Logf("poids d'un COMPTEUR : %d o (« %s »), %d o (« %s »)",
		poidsCourt, tPortUsine(12), poidsLong, tPortRenomme(12))

	// Les chiffres written dans snmp.go et dans worstBytesPerSample cote controleur.
	// S'ils ne tiennent plus, ce sont les COMMENTAIRES qu'il faut corriger avant les
	// constantes — un plafond justified par une mesure stale est exactement le default
	// que ce lot repare. Les seuils laissent le bruit d'un chiffre de compteur en
	// plus, pas la place d'une tag supplementaire.
	if poidsCourt > 200 || poidsLong > 240 {
		t.Errorf("un compteur pese %d o (nommage d'usine, 196 documentes) et %d o "+
			"(label de 37 caracteres, 229 documentes) : la forme des samples a "+
			"grossi, revoyez les commentaires de maxPerFlush et worstBytesPerSample",
			poidsCourt, poidsLong)
	}

	// Le lot actually envoye, dans le cas le plus lourd.
	var body = corpsDuLot(t, etireLot(long, maxPerFlush))
	t.Logf("lot de %d samples = %d octets (%d KiB), soit %d %% du plafond de "+
		"corps de %d KiB", maxPerFlush, len(body), len(body)/1024,
		100*len(body)/controllerBodyCapBytes, controllerBodyCapBytes/1024)
	if len(body) >= controllerBodyCapBytes {
		t.Fatalf("lot de %d samples = %d octets : le controleur le REFUSERA "+
			"(plafond de corps %d) et le proxy l'ABANDONNERA a chaque flush — la "+
			"remontee deviendrait un gaspillage journalise",
			maxPerFlush, len(body), controllerBodyCapBytes)
	}

	// Le mur, calcule sur la mesure. Deux candidats, le plus bas gagne — et c'est
	// aujourd'hui le nombre, pas les octets. Le dire dans le journal du test est la
	// half de son interet : celui qui viendra monter `maxPerFlush` lira le chiffre
	// au lieu de le deduire.
	var murParOctets = controllerBodyCapBytes / poidsLong
	var mur = min(controllerMaxSamples, murParOctets)
	t.Logf("mur de maxPerFlush : %d samples — %d par le garde-fou de nombre, "+
		"%d par le plafond de corps a %d o/sample ; reglage actuel %d (%d %% du mur)",
		mur, controllerMaxSamples, murParOctets, poidsLong, maxPerFlush,
		100*maxPerFlush/mur)
	if maxPerFlush > mur {
		t.Fatalf("maxPerFlush=%d franchit le mur du controleur (%d) : tout lot plein "+
			"sera refused definitivement, donc abandonne", maxPerFlush, mur)
	}
	// Une marge, pas un passage de justesse. Le poids par sample depend d'un
	// label que l'exploitant choisit et qu'aucun code ne borne : sous 75 % du mur, un
	// allongement d'un tiers des noms de ports passe encore.
	if maxPerFlush > mur*3/4 {
		t.Errorf("maxPerFlush=%d depasse 75 %% du mur de %d : marge trop mince pour "+
			"des labels de ports plus longs que ceux mesures", maxPerFlush, mur)
	}
}

// TestDebitSoutenable documente ce que la remontee encaisse AFTER le reglage, et
// a quel prix. Les trois assertions sont les trois arbitrages du lot ; en changer
// une seule sans changer le chiffre en face est une regression silencieuse.
func TestDebitSoutenable(t *testing.T) {
	// ~45 samples/s par 48 ports : established par TestProductionParEquipement.
	const parEquipement = 45
	var soutenable = sustainedSamplesPerSecond()
	var equipements = soutenable / parEquipement
	t.Logf("rate soutenable %d samples/s = %d devices de 48 ports "+
		"(%d lots de %d toutes les %v)",
		soutenable, equipements, maxFlushBurst, maxPerFlush, flushEvery)

	// Avant ce lot : 5000 / 10 s = 500/s, soit 11 devices. Le seuil retenu ici
	// est deliberately rond et bas devant les 133 computed : il dit « la remontee
	// doit tenir un parc de site entier », pas « elle doit valoir exactement 6000 ».
	if equipements < 100 {
		t.Errorf("%d devices de 48 ports seulement (%d samples/s) : "+
			"la cadence a 5 s ne remonterait pas sur un parc de site", equipements, soutenable)
	}

	// L'autre plateau de la balance : le cost en requests HTTP au controleur. Au
	// plafond de rate, et pas en regime ordinaire — un tampon vide ne produit
	// aucune request, `flush` sortant avant l'envoi.
	var requetesParSeconde = float64(maxFlushBurst) / flushEvery.Seconds()
	t.Logf("au plafond : %.1f request/s vers le controleur", requetesParSeconde)
	if requetesParSeconde > 3 {
		t.Errorf("%.1f request/s au plafond : un flush plus frequent multiplie les "+
			"requests, ce n'est pas gratuit pour le controleur", requetesParSeconde)
	}

	// Le tampon, une fois le rate suffisant, ne sert plus only couvrir une coupure
	// de TUNNEL. Sa taille se lit donc en secondes d'indisponibilite, et sa borne
	// haute est la memory d'un proxy qui tourne sur un Raspberry Pi.
	//
	// 401 o/sample en tas : mesure (runtime.MemStats sur 200 000 samples
	// avec leur carte d'tags). L'essentiel est la carte, pas la valeur.
	const octetsEnTas = 401
	var couvertureAuPlafond = bufCapacity / soutenable
	var memoire = bufCapacity * octetsEnTas / (1 << 20)
	t.Logf("tampon %d samples : %d s de coupure au plafond de rate, "+
		"%d s pour un parc de 12 devices, ~%d Mio de memory au pire",
		bufCapacity, couvertureAuPlafond, bufCapacity/(12*parEquipement), memoire)
	if couvertureAuPlafond < 20 {
		t.Errorf("tampon trop court : %d s de coupure couverte au plafond, "+
			"une reconnexion de tunnel dure plus que ca", couvertureAuPlafond)
	}
	if memoire > 80 {
		t.Errorf("tampon a ~%d Mio : trop pour un proxy sur VM minimale", memoire)
	}
}

// TestSaturationSeVoitEtSAmortit : une perte par SATURATION est le symptome que
// la cadence a 5 s rend possible, et c'est le plus traitre — le tunnel est debout,
// les requests reussissent, `flush` n'a rien a signaler, et il ne reste que des
// trous dans les courbes. Elle doit donc se dire tout de suite, puis se taire.
func TestSaturationSeVoitEtSAmortit(t *testing.T) {
	// Tampon minuscule, construit a la main : ce qu'on tests est la mise en mots
	// de la perte, pas la capability reelle — remplir 150 000 samples couterait
	// 57 Mio pour exactement la same assertion.
	var c = &SNMPCollector{buf: metricbuf.New(10)}

	// Rien de perdu : rien a dire, et state d'amortissement ne bouge pas. Sans
	// cette condition, la toute first ligne du journal d'un proxy sain
	// annoncerait une saturation a zero point perdu.
	var at, dropped = c.reportSaturation(time.Time{}, 0)
	if !at.IsZero() || dropped != 0 {
		t.Fatalf("sans perte, rien ne doit be annonce : %v / %d", at, dropped)
	}

	// Quinze samples dans un tampon de dix : cinq points jetes.
	var i int
	for i = 0; i < 15; i++ {
		c.buf.Add(metricbuf.Sample{Name: "meshcraft_if_in_octets_total"})
	}
	if at, dropped = c.reportSaturation(time.Time{}, 0); at.IsZero() || dropped != 5 {
		t.Fatalf("first perte : annonce immediate de 5 points attendue, "+
			"obtenu %v / %d", at, dropped)
	}

	// La perte continue, mais on vient de le dire. Amorti — et le cumul already
	// annonce est CONSERVE tel quel, sans quoi la prochaine ligne ne pourrait plus
	// annoncer la period entire.
	for i = 0; i < 4; i++ {
		c.buf.Add(metricbuf.Sample{Name: "meshcraft_if_in_octets_total"})
	}
	var at2, dropped2 = c.reportSaturation(at, dropped)
	if !at2.Equal(at) || dropped2 != dropped {
		t.Fatalf("amortissement expected, obtenu %v / %d", at2, dropped2)
	}

	// Passe le delai, la ligne repart. Le cumul rendu est celui du tampon (9), et
	// la ligne annonce la difference — les 4 points de la period, pas les 9 depuis
	// le demarrage : « 4 perdus dans la last minute » se decide, « 9 depuis le
	// demarrage » se subit.
	if at2, dropped2 = c.reportSaturation(at.Add(-dropReportEvery),
		dropped); dropped2 != 9 {
		t.Fatalf("after amortissement : cumul de 9 expected, obtenu %d", dropped2)
	}
	if !at2.After(at) {
		t.Errorf("l'instant du dernier dit doit avancer, obtenu %v", at2)
	}
}

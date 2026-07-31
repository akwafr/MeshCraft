package driver

import (
	"testing"
	"time"
)

// TestBestChoisitLeCanalLePlusRapide : planifier, c'est demander le rythme que
// the device tient — pas celui qu'on voudrait. Best rend le canal le plus
// rapide qui sait rendre la metrique.
func TestBestChoisitLeCanalLePlusRapide(t *testing.T) {
	var caps = MonitoringCapabilities{Channels: []MonitorChannel{
		{Kind: "web", Metrics: []string{"if_state"}, MinInterval: 30 * time.Second},
		{Kind: "snmp", Metrics: []string{"if_state"}, MinInterval: 5 * time.Second},
		{Kind: "snmp", Metrics: []string{"if_counters"}, MinInterval: 30 * time.Second},
	}}

	var c, ok = caps.Best("if_state")
	if !ok || c.Kind != "snmp" || c.MinInterval != 5*time.Second {
		t.Errorf("if_state doit passer par SNMP a 5 s, obtenu %+v (ok=%v)", c, ok)
	}
	if c, ok = caps.Best("if_counters"); !ok || c.MinInterval != 30*time.Second {
		t.Errorf("if_counters expected a 30 s, obtenu %+v", c)
	}
	// Une metrique qu'aucun canal ne rend n'est pas supervisee, et il faut le
	// dire : un parc a half supervise sans que personne ne le voie est le pire
	// des results.
	if _, ok = caps.Best("streaming_only_metric"); ok {
		t.Error("une metrique sans canal doit be flagged absente, pas invented")
	}
}

// TestBestSansCanal : une plateforme sans observability declared ne doit rien
// promettre. La capability inconnue est supposee ABSENTE.
func TestBestSansCanal(t *testing.T) {
	var _, ok = (MonitoringCapabilities{}).Best("if_state")
	if ok {
		t.Error("aucune capability declared ne doit valoir aucune supervision")
	}
}

// TestWhyUnavailableSepareLIgnoranceDuFaitEtabli : les deux responses possibles de
// WhyUnavailable ne disent pas la same chose. Un motif declared est un fait sur
// the device ; l'absence de motif n'est PAS une disponibilite, c'est
// simplement que personne ne l'a documente. Confondre les deux referait le
// silence que ce champ existe pour rompre.
func TestWhyUnavailableSepareLIgnoranceDuFaitEtabli(t *testing.T) {
	var caps = MonitoringCapabilities{
		Channels: []MonitorChannel{
			{Kind: "snmp", Metrics: []string{"if_state"}, MinInterval: 5 * time.Second},
		},
		Unavailable: []MetricUnavailable{
			{Metric: "optical_power", Observed: true, Source: "observed"},
			{Metric: "temperature"},
		},
	}

	var u, ok = caps.WhyUnavailable("optical_power")
	if !ok || !u.Observed || u.Source == "" {
		t.Errorf("le motif observed doit remonter tel quel : %+v (ok=%v)", u, ok)
	}
	// Constate / suppose doit se DISTINGUER : sans ca, the UI affiche du same ton
	// un fait mesure et une deduction, et fait renoncer un exploitant sur une
	// supposition.
	if u, ok = caps.WhyUnavailable("temperature"); !ok || u.Observed {
		t.Errorf("une absence supposee ne doit pas se present  comme observed : %+v", u)
	}
	// Metrique dont personne n'a documente l'absence : pas de motif, et surtout
	// aucune promesse de disponibilite — c'est Best qui en decide.
	if _, ok = caps.WhyUnavailable("health"); ok {
		t.Error("aucun motif n'est declared pour health : en inventer un serait pire que rien")
	}
	if _, ok = caps.Best("health"); ok {
		t.Error("health n'a aucun canal : la metrique n'est pas supervisee")
	}
}

// TestContradictionsAttrapeLaDeclarationQuiSeMord : declare une metrique a la
// fois rendue par un canal et indisponible sur la plateforme est le seul default
// que cette structure permet encore. Il faut qu'il se voie, parce qu'il produit
// soit une courbe promise que rien ne remplira, soit un motif d'absence a cote
// d'une courbe bien vivante.
func TestContradictionsAttrapeLaDeclarationQuiSeMord(t *testing.T) {
	var sain = MonitoringCapabilities{
		Channels: []MonitorChannel{
			{Kind: "snmp", Metrics: []string{"if_state"}, MinInterval: 5 * time.Second},
		},
		Unavailable: []MetricUnavailable{{Metric: "optical_power"}},
	}
	var consistent = sain.Contradictions()
	if len(consistent) != 0 {
		t.Errorf("declaration consistent attendue sans contradiction, obtenu %v", consistent)
	}

	var mordue = MonitoringCapabilities{
		Channels: []MonitorChannel{
			{Kind: "snmp", Metrics: []string{"if_state", "optical_power"},
				MinInterval: 5 * time.Second},
		},
		Unavailable: []MetricUnavailable{{Metric: "optical_power"}},
	}
	var got = mordue.Contradictions()
	if len(got) != 1 || got[0] != "optical_power" {
		t.Errorf("la contradiction doit be named, obtenu %v", got)
	}
}

// TestBestOfKindNeLaissePasUnCanalDeciderPourUnAutre
//
// # Le default que cette method corrige, et qui n'was pas hypothetique
//
// `Best` rend le canal le plus rapide TOUS GENRES CONFONDUS. C'est la bonne response a
// « a quelle vitesse cette plateforme peut-elle be observee » — la question qu'un UI
// pose — mais pas a « a quelle vitesse ai-je le droit d'interroger ce canal-ci », qui est
// la question de la PLANIFICATION.
//
// La difference coutait cher, et elle was verifiable : le collecteur periodique du
// produit s'execute sur le proxy et ne parle QUE SNMP. Sur une plateforme declarant un
// canal web rapide et un canal SNMP lent — le cas du connecteur RouterOS, dont la REST
// est mesuree a 111 ms et dont le canal SNMP n'a jamais servi un seul OID — `Best`
// rendait le plancher de la REST, et le controleur l'appliquait a un walk SNMP. Le
// plancher d'un canal was oppose a un AUTRE canal, ce qui est tres exactement ce qu'un
// plancher n'est pas.
func TestBestOfKindNeLaissePasUnCanalDeciderPourUnAutre(t *testing.T) {
	var caps = MonitoringCapabilities{Channels: []MonitorChannel{
		{Kind: "web", Metrics: []string{"if_state"}, MinInterval: 5 * time.Second},
		{Kind: "snmp", Metrics: []string{"if_state"}, MinInterval: 60 * time.Second},
		{Kind: "api", Metrics: []string{"if_state"}, MinInterval: 30 * time.Second},
	}}

	// La question de the UI : le plus rapide, tous genres confondus.
	var global, ok = caps.Best("if_state")
	if !ok || global.Kind != "web" || global.MinInterval != 5*time.Second {
		t.Errorf("Best doit rendre le canal le plus rapide toutes gammes confondues : %+v",
			global)
	}

	// La question de la planification : le plancher DU canal qu'on va emprunter.
	var cas = []struct {
		genre    string
		plancher time.Duration
	}{
		{"web", 5 * time.Second},
		{"api", 30 * time.Second},
		{"snmp", 60 * time.Second},
	}
	var c int
	for c = range cas {
		var ch, trouve = caps.BestOfKind(cas[c].genre, "if_state")
		if !trouve {
			t.Errorf("aucun canal %q pour if_state", cas[c].genre)
			continue
		}
		if ch.MinInterval != cas[c].plancher {
			t.Errorf("plancher du canal %q : %v expected %v — un canal d'un autre genre "+
				"ne doit pas pouvoir decider a sa place",
				cas[c].genre, ch.MinInterval, cas[c].plancher)
		}
	}
}

// TestBestOfKindRetiendLePlusRapideDuGENRE
//
// Deux canaux du same genre pour la same metrique existent : le connecteur RouterOS en a
// deux en `api` — l'un pour state et les compteurs, l'autre pour l'optique — et rien
// n'interdit qu'une plateforme en declares deux pour la same metrique a des costs
// differents. C'est alors le PLUS RAPIDE du genre qui borne, comme pour `Best`.
func TestBestOfKindRetiendLePlusRapideDuGENRE(t *testing.T) {
	var caps = MonitoringCapabilities{Channels: []MonitorChannel{
		{Kind: "snmp", Metrics: []string{"health"}, MinInterval: 120 * time.Second},
		{Kind: "snmp", Metrics: []string{"health"}, MinInterval: 60 * time.Second},
	}}
	var ch, ok = caps.BestOfKind("snmp", "health")
	if !ok || ch.MinInterval != 60*time.Second {
		t.Errorf("le plus rapide du genre expected (60 s), obtenu %+v", ch)
	}
}

// TestBestOfKindNeReplieurPasToutSeul
//
// Le repli est laisse a l'APPELANT, deliberately. Une plateforme qui ne declares aucun
// canal du genre requested doit be traitee EXPLICITEMENT — soit en retombant sur le
// plancher de l'inconnu, soit en ne collectant pas du tout — et masquer ce choix derriere
// un repli silencieux ferait interroger en SNMP une plateforme qui n'en a jamais annonce
// la capability.
func TestBestOfKindNeReplieurPasToutSeul(t *testing.T) {
	var caps = MonitoringCapabilities{Channels: []MonitorChannel{
		{Kind: "web", Metrics: []string{"if_state"}, MinInterval: 10 * time.Second},
	}}
	var ch, ok = caps.BestOfKind("snmp", "if_state")
	if ok {
		t.Errorf("aucun canal SNMP n'est declared : BestOfKind ne doit PAS se rabattre "+
			"sur le canal web, obtenu %+v", ch)
	}
	// Et une metrique inconnue du genre requested ne se replie pas non plus sur une autre
	// metrique du same canal.
	var _, ok2 = caps.BestOfKind("web", "optical_power")
	if ok2 {
		t.Error("une metrique non portee par le canal ne doit pas be trouvee")
	}
}

package swos

import (
	"math"
	"testing"

	"meshcraft/internal/metricbuf"
)

// La telemetry de SwOS, verified sur les CAPTURES REELLES de the device.
//
// Ces tests ont une property que ceux du connecteur RouterOS voisin n'ont pas : ici,
// TOUT est observed. Le scraping de pages est le seul canal de cette plateforme, il a been
// exerce, et les quatre pages dont la telemetry a besoin — /link.b, /stats.b, /sys.b,
// /sfp.b — sont dans testdata/ telles que le CSS326-24G-2S+ de reference les a servies.
//
// Ce qui rend ces tests utiles au-dela de la non-regression : les valeurs attendues
// ci-dessous sont COMPUTED a la main depuis les octets de la capture, avec leur
// justification. Une echelle qui changerait ferait echouer un test qui dit pourquoi le
// chiffre was celui-there — et non un test qui compare deux nombres dont personne ne se
// souvient de l'origine.

// pagesDeReference charge les quatre pages de la capture.
func pagesDeReference(t *testing.T) (link, stats, sys, sfp *Page) {
	t.Helper()
	var err error
	link, err = ParseObject(capture(t, "link.b"))
	if err != nil {
		t.Fatalf("/link.b refused : %v", err)
	}
	stats, err = ParseObject(capture(t, "stats.b"))
	if err != nil {
		t.Fatalf("/stats.b refused : %v", err)
	}
	sys, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b refused : %v", err)
	}
	sfp, err = ParseObject(capture(t, "sfp.b"))
	if err != nil {
		t.Fatalf("/sfp.b refused : %v", err)
	}
	return link, stats, sys, sfp
}

// telemetrieDeReference reconstruit the snapshot exactement comme ReadTelemetry le
// ferait sur le hardware, mais sans socket : la geometry sort de /link.b, comme sur
// the device.
func telemetrieDeReference(t *testing.T) Telemetry {
	t.Helper()
	var link, stats, sys, sfp = pagesDeReference(t)
	var ports, cages, first, err = geometryOf(link)
	if err != nil {
		t.Fatalf("geometry illisible : %v", err)
	}
	// Releve : `prt: 0x1a` (26 ports), `sfp: 0x02` (2 cages), `sfpo: 0x18` (la first
	// cage est le port d'index 24, donc le port numerote 25).
	if ports != 26 || cages != 2 || first != 24 {
		t.Fatalf("geometry inattendue : %d ports, %d cage(s), first a %d",
			ports, cages, first)
	}
	var out = Telemetry{}
	out.Ports = foldLinkTelemetry(link, NewFacade(ports, cages, first, DefaultPortPrefix))
	foldSys(sys, &out)
	foldStats(stats, out.Ports)
	out.Optics = foldOptics(sfp, cages, NewFacade(ports, cages, first, DefaultPortPrefix))
	return out
}

// TestEtatDesPortsLuSurLaCaptureReelle
//
// Releve : `lnk: 0x02000000` — le bit 25 seul, donc la cage SFP2 est le SEUL port qui
// porte un lien. `en: 0x03ffffff` — les vingt-six ports sont administrativement actifs.
func TestEtatDesPortsLuSurLaCaptureReelle(t *testing.T) {
	var tel = telemetrieDeReference(t)
	if len(tel.Ports) != 26 {
		t.Fatalf("%d port(s) lus, 26 attendus", len(tel.Ports))
	}

	var avecLien []string
	var p PortTelemetry
	for _, p = range tel.Ports {
		if p.LinkUp {
			avecLien = append(avecLien, p.Name)
		}
		if !p.Enabled {
			t.Errorf("%s declared inactif alors que `en: 0x03ffffff` active les 26 ports",
				p.Name)
		}
	}
	if len(avecLien) != 1 || avecLien[0] != "SFP2" {
		t.Errorf("ports avec lien : %v — la capture porte `lnk: 0x02000000`, donc le "+
			"bit 25, donc le port NUMEROTE 26", avecLien)
	}
	// Les noms que the device donne, rendus a cote et jamais a la place : « Port1 »…
	// « SFP2 » par default, mais du texte libre en realite.
	if tel.Ports[0].DeviceName != "Port1" || tel.Ports[25].DeviceName != "SFP2" {
		t.Errorf("noms de the device mal lus : %q … %q",
			tel.Ports[0].DeviceName, tel.Ports[25].DeviceName)
	}
}

// TestDebitNeguocieSeulementSurUnPortAvecLien — le trap de l'index 7.
//
// Releve : les 25 ports SANS lien portent l'index de rate 7, qui est HORS de la table
// `SpeedLabels` (sept entries, indices 0 a 6), et le seul port avec lien porte 3,
// i.e. 10G — ce que confirme le transceiver insere (`SFP-10G-BX`).
//
// Un index hors table n'est donc pas « 10M par default », c'est une ABSENCE. La rendre
// comme une valeur ferait afficher un rate sur vingt-cinq prises vides.
func TestDebitNeguocieSeulementSurUnPortAvecLien(t *testing.T) {
	var tel = telemetrieDeReference(t)
	var p PortTelemetry
	for _, p = range tel.Ports {
		if p.LinkUp {
			if p.SpeedMbps == nil || *p.SpeedMbps != 10000 {
				t.Errorf("%s porte un lien : rate %v expected 10000 Mbit/s (index 3 = 10G)",
					p.Name, p.SpeedMbps)
			}
			if p.FullDuplex == nil || !*p.FullDuplex {
				t.Errorf("%s : duplex %v — `dpx: 0x02000000` marque le bit 25",
					p.Name, p.FullDuplex)
			}
			continue
		}
		if p.SpeedMbps != nil {
			t.Errorf("%s n'a pas de lien mais porte un rate de %v Mbit/s : l'index 7 "+
				"observed sur ces ports est HORS table, c'est une absence", p.Name, *p.SpeedMbps)
		}
		if p.FullDuplex != nil {
			t.Errorf("%s n'a pas de lien mais porte un duplex : « demi-duplex » sur un "+
				"port debranche fait chercher un desaccord de negociation", p.Name)
		}
	}
}

// TestCompteursDeStatsSurLeSeulPortActif — les valeurs, computed depuis les octets.
//
//	rb[25]  = 0x018f2a86 = 26 159 750 octets recus
//	rbh[25] = 0x00000000 → la half haute est nulle, le compteur tient sur 32 bits
//	tb[25]  = 0x00218659 =  2 197 081 octets emis
//	rtp[25] = 0x0005ed25 =    388 389 trames recues
//	ttp[25] = 0x00001f58 =      8 024 trames emises
func TestCompteursDeStatsSurLeSeulPortActif(t *testing.T) {
	var tel = telemetrieDeReference(t)
	var cage = tel.Ports[25]

	var cas = []struct {
		nom      string
		got      *float64
		expected float64
	}{
		{"octets recus", cage.InOctets, 26159750},
		{"octets emis", cage.OutOctets, 2197081},
		{"trames recues", cage.InPackets, 388389},
		{"trames emises", cage.OutPackets, 8024},
	}
	var c int
	for c = range cas {
		if cas[c].got == nil {
			t.Errorf("%s : non lu", cas[c].nom)
			continue
		}
		if *cas[c].got != cas[c].expected {
			t.Errorf("%s : %v expected %v", cas[c].nom, *cas[c].got, cas[c].expected)
		}
	}
	// Aucune erreur sur cet device au repos, et c'est une valeur LUE et non une
	// absence : le champ existe, il vaut zero, l'sample dit quelque chose de vrai.
	if cage.InErrors == nil || *cage.InErrors != 0 {
		t.Errorf("erreurs de receive : %v — la capture porte `rte` a zero, ce qui est "+
			"un fait, pas une absence", cage.InErrors)
	}
}

// TestCompteur64BitsRecomposeLesDeuxMoities
//
// `rb` porte les 32 bits bas, `rbh` les 32 hauts. Lire `rb` seul ferait repasser le
// compteur a zero tous les 4 Gio — sur un lien 10G, toutes les trois secondes et demie,
// ce qui produirait une courbe de trafic entirely fausse. Ce connecteur supervise des
// cages 10G : ce n'est pas une precaution theorique.
//
// La capture de reference a sa half haute a zero, donc elle ne peut PAS prouver la
// recomposition. Le test la force sur une page fabriquee — c'est le seul cas de ce
// fichier where la capture ne suffit pas, et il faut le dire rather que de croire le test
// couvrant.
func TestCompteur64BitsRecomposeLesDeuxMoities(t *testing.T) {
	var page = NewPage()
	page.SetNums(FieldRxBytes, []uint32{0x00000001})
	page.SetNums(FieldRxBytesHigh, []uint32{0x00000003})
	var ports = []PortTelemetry{{Name: "Port1"}}
	foldStats(page, ports)

	// 3 × 2³² + 1 = 12 884 901 889.
	var expected = float64(uint64(3)<<32 | 1)
	if ports[0].InOctets == nil || *ports[0].InOctets != expected {
		t.Errorf("compteur 64 bits : %v expected %v — la half haute n'a pas been "+
			"recomposee, et la courbe reviendrait a zero tous les 4 Gio",
			ports[0].InOctets, expected)
	}
	// Et le pendant : une half haute plus COURTE que la basse est traitee comme un
	// zero, pas comme un refus. C'est ce qu'un firmware qui n'a pas encore depasse
	// 4 Gio sur ce port rendrait, et refuser le compteur entier pour ca priverait de
	// trafic un device parfaitement lisible.
	var page2 = NewPage()
	page2.SetNums(FieldRxBytes, []uint32{0x000000ff, 0x000000ee})
	page2.SetNums(FieldRxBytesHigh, []uint32{0x00000000})
	var ports2 = []PortTelemetry{{Name: "Port1"}, {Name: "Port2"}}
	foldStats(page2, ports2)
	if ports2[1].InOctets == nil || *ports2[1].InOctets != 0xee {
		t.Errorf("half haute tronquee : %v expected 238", ports2[1].InOctets)
	}
}

// TestDebitInstantaneEtLaCorroborationDeSonEchelle
//
// Scale (division par 100) est observed dans l'embedded interface (`scale:.01`), et
// elle est CORROBORATED par les compteurs de la same capture, ce qui est plus solide qu'une
// lecture de JavaScript. Le calcul, sur le seul port qui porte un lien :
//
//	octets recus       rb  = 0x018f2a86 = 26 159 750
//	temps de marche    upt = 0x00e4cf41 = 14 995 265 hundredths = 149 952,65 s
//	rate MOYEN                          ≈ 1 396 bit/s
//	rate snapshot   rrb = 0x0000145f = 5 215 → /100 = 52,15 bit/s
//
// Snapshot est bien SOUS la moyenne, ce qui est consistent avec un lien de laboratoire
// calme after une campagne d'adoption. L'autre hypothese — un facteur ×100, donc
// 521 500 bit/s — impliquerait 9,8 Go transferes sur la duree de marche, contre 26 Mo
// actually comptes : elle est ecartee par trois ordres de grandeur.
//
// Ce test VERIFIE la corroboration, il ne se contente pas de la citer : si the scale
// changeait, c'est l'incoherence avec les compteurs qu'il montrerait.
func TestDebitInstantaneEtLaCorroborationDeSonEchelle(t *testing.T) {
	var tel = telemetrieDeReference(t)
	var cage = tel.Ports[25]

	if cage.InBitsPerSecond == nil {
		t.Fatal("rate snapshot non lu : c'est lui qui rend un plancher de 15 s " +
			"acceptable sur cette plateforme")
	}
	if math.Abs(*cage.InBitsPerSecond-52.15) > 0.001 {
		t.Errorf("rate snapshot : %v bit/s expected 52,15 (0x145f / 100)",
			*cage.InBitsPerSecond)
	}

	// La corroboration : le rate moyen deduit des compteurs doit be du SAME ORDRE DE
	// GRANDEUR que the snapshot, a un facteur cent pres au plus. Un facteur cent
	// d'ecart serait le symptome exact d'une echelle fausse.
	if tel.UptimeSeconds == nil || *tel.UptimeSeconds <= 0 {
		t.Fatal("temps de marche non lu : la corroboration est impossible")
	}
	var moyen = *cage.InOctets * 8 / *tel.UptimeSeconds
	var rapport = moyen / *cage.InBitsPerSecond
	if rapport > 100 || rapport < 0.01 {
		t.Errorf("rate moyen deduit des compteurs (%.0f bit/s) et rate snapshot "+
			"(%.2f bit/s) separes par un facteur %.0f : c'est le symptome d'une echelle "+
			"fausse sur `rrb`, pas d'un lien calme", moyen, *cage.InBitsPerSecond, rapport)
	}
}

// TestSanteReduiteAUneTemperatureEtUnUptime
//
//	temp = 0x45 = 69 °C, en degrees ENTIERS et sans facteur d'echelle. Relevee a 0x44
//	              puis 0x45 a quelques minutes d'intervalle sur the device : c'est un
//	              capteur vivant, pas une constante de firmware.
//	upt  = 0x00e4cf41 = 14 995 265 HUNDREDTHS de seconde = 149 952,65 s. Lire ce champ
//	              comme des secondes donnerait un temps de fonctionnement cent fois trop
//	              grand — cinq mois there where the device est debout depuis deux jours.
func TestSanteReduiteAUneTemperatureEtUnUptime(t *testing.T) {
	var tel = telemetrieDeReference(t)

	if tel.TemperatureC == nil || *tel.TemperatureC != 69 {
		t.Errorf("temperature de puce : %v expected 69 °C (0x45, degrees entiers)",
			tel.TemperatureC)
	}
	if tel.UptimeSeconds == nil {
		t.Fatal("temps de fonctionnement non lu")
	}
	if math.Abs(*tel.UptimeSeconds-149952.65) > 0.01 {
		t.Errorf("temps de fonctionnement : %v s expected 149 952,65 s — le champ est en "+
			"HUNDREDTHS de seconde", *tel.UptimeSeconds)
	}
	// Les capteurs optionnels sont TOUS a zero sur ce model (ventilateurs, tensions,
	// temperatures de carte) et ne doivent produire AUCUNE ingress : quatre series
	// « ventilateur a 0 tr/min » sur tout le parc SwOS seraient une alerte permanente
	// et fausse.
	if len(tel.Sensors) != 0 {
		t.Errorf("%d capteur(s) optionnel(s) rendus alors que la capture les sert tous "+
			"a zero : %+v", len(tel.Sensors), tel.Sensors)
	}
}

// TestUnCapteurNonNulEstRenduLuiAussi — la branche symetrique du filtre sur zero.
//
// Le filtre du test previous est une HEURISTIQUE imposee par la plateforme : SwOS ne
// distingue pas « ce capteur n'existe pas » de « ce capteur vaut zero ». Il faut donc
// verify qu'il ne jette pas tout — un chassis qui servirait une vraie vitesse de
// ventilateur doit la voir apparaitre.
//
// Et la limite que ce filtre creates est named there where elle vit (foldSysSensors) : un
// ventilateur ACTUALLY STOPPED vaut zero, donc devient indiscernable d'un ventilateur
// absent. Aucun code ne peut lever cette ambiguite ; ce qui la contient partiellement
// est la TEMPERATURE, qui monte quand le ventilateur stops.
func TestUnCapteurNonNulEstRenduLuiAussi(t *testing.T) {
	var page = NewPage()
	page.SetNum(FieldTemp, 45)
	page.SetNum(FieldUptime, 100)
	page.SetNum("fan1", 4200)
	var tel Telemetry
	foldSys(page, &tel)

	if len(tel.Sensors) != 1 {
		t.Fatalf("%d capteur(s) rendus, 1 expected : %+v", len(tel.Sensors), tel.Sensors)
	}
	if tel.Sensors[0].Name != "fan1" || tel.Sensors[0].Type != "RPM" ||
		tel.Sensors[0].Value != 4200 {
		t.Errorf("capteur mal rendu : %+v", tel.Sensors[0])
	}
}

// TestDiagnostiqueOptiqueEtSonIndEXATION — le seul vrai trap de /sfp.b.
//
// Ses tableaux sont indexes par CAGE et non par port : DEUX entries, alors que /stats.b
// en porte vingt-six. La cage de rang `i` est le port d'index `sfpo + i` — observed
// `sfpo: 0x18`, donc la first cage est le port numerote 25. Recoller les deux
// indexations en supposant qu'elles coincident attribuerait le diagnostic optique de
// SFP1 au port 1, i.e. a une prise cuivre.
//
// Les unites, toutes observed dans l'embedded interface :
//
//	tmp  0x32       = 50 °C, degrees entiers
//	vcc  0x0cbd     = 3 261 mV → 3,261 V (scale:1E3)
//	tbs  0x22       = 34 mA
//	wln  0x0001f018 = 127 000 hundredths de nm → 1 270 nm, ce que confirme le champ
//	                  descriptif de la SAME page (`typ: '1270nm & smf'`)
//	tpw  0x12b6     = 4 790 dixiemes de µW → 10·log₁₀(0,479) = −3,1966 dBm
//	rpw  0x0ad2     = 2 770 dixiemes de µW → 10·log₁₀(0,277) = −5,5752 dBm
func TestDiagnostiqueOptiqueEtSonIndEXATION(t *testing.T) {
	var tel = telemetrieDeReference(t)
	if len(tel.Optics) != 2 {
		t.Fatalf("%d cage(s), 2 attendues", len(tel.Optics))
	}

	// L'INDEXATION : la first cage est le port 25, la seconde le port 26.
	if tel.Optics[0].Port != "SFP1" || tel.Optics[1].Port != "SFP2" {
		t.Fatalf("cages rattachees a %q et %q — `sfpo: 0x18` place la first au port "+
			"NUMEROTE 25. Les rattacher a Port1 et Port2 afficherait une puissance optique "+
			"sur une prise cuivre", tel.Optics[0].Port, tel.Optics[1].Port)
	}

	// La cage 1 est VIDE : `tmp[0] = 0xffffff80`, i.e. -128 °C, qui est le
	// « pas de module » de l'interface et non une temperature.
	if tel.Optics[0].Present {
		t.Error("la cage 1 est vide dans la capture (`tmp: 0xffffff80` = -128 °C) : la " +
			"traiter comme un module ferait apparaitre un capteur a -128 °C sur tout " +
			"switch dont les cages sont libres")
	}
	if tel.Optics[0].TemperatureC != nil {
		t.Errorf("la cage vide porte une temperature de %v °C", *tel.Optics[0].TemperatureC)
	}
	// Et son identity est effacee : les champs texte d'une cage vide sont des strings
	// d'espaces, qui feraient un transceiver fantome dans l'inventaire.
	if tel.Optics[0].Vendor != "" || tel.Optics[0].Part != "" {
		t.Errorf("la cage vide porte une identity : %+v", tel.Optics[0])
	}

	var m = tel.Optics[1]
	if !m.Present {
		t.Fatal("la cage 2 porte un module (`tmp: 0x32`)")
	}
	// Identity est ROGNEE de son remplissage : les champs du SFF-8472 sont de taille
	// fixe et rembourres par des espaces (`'46532020…'` = « FS » + quatorze espaces).
	if m.Vendor != "FS" || m.Part != "SFP-10G-BX" || m.Serial != "S2001001227" {
		t.Errorf("identity du module mal rognee : vendeur %q, reference %q, serial %q",
			m.Vendor, m.Part, m.Serial)
	}

	var cas = []struct {
		nom      string
		got      *float64
		expected float64
		tol      float64
	}{
		{"temperature du module", m.TemperatureC, 50, 0.001},
		{"tension d'alimentation", m.SupplyVolts, 3.261, 0.0001},
		{"courant de polarisation", m.TxBiasMA, 34, 0.001},
		{"longueur d'onde", m.WavelengthNM, 1270, 0.001},
		{"puissance emise", m.TxPowerDBM, -3.1966, 0.001},
		{"puissance recue", m.RxPowerDBM, -5.5752, 0.001},
	}
	var c int
	for c = range cas {
		if cas[c].got == nil {
			t.Errorf("%s : non lue", cas[c].nom)
			continue
		}
		if math.Abs(*cas[c].got-cas[c].expected) > cas[c].tol {
			t.Errorf("%s : %v expected %v (± %v)",
				cas[c].nom, *cas[c].got, cas[c].expected, cas[c].tol)
		}
	}
}

// TestPuissanceNulleNeProduitAucuneValeurEnDBM
//
// log₁₀(0) n'existe pas. Une cage sous tension mais sans lumiere — fibre debranchee,
// device d'en face off — ne doit donc produire AUCUN sample rather que moins
// l'infini : un point a -Inf dans VictoriaMetrics ecrase the scale du graphe entier, si
// bien qu'une seule fibre debranchee rendrait illisibles les courbes de tous les liens
// qui fonctionnent.
func TestPuissanceNulleNeProduitAucuneValeurEnDBM(t *testing.T) {
	var dst *float64
	assignDBM(&dst, []uint32{0}, 0)
	if dst != nil {
		if math.IsInf(*dst, -1) {
			t.Error("une puissance nulle a produit moins l'infini : ce point ecrase " +
				"the scale de tout le graphe")
		}
		t.Errorf("une puissance nulle a produit %v", *dst)
	}
	// Et une valeur non nulle passe bien.
	assignDBM(&dst, []uint32{10000}, 0)
	if dst == nil || math.Abs(*dst) > 0.0001 {
		t.Errorf("10 000 dixiemes de µW valent 1 mW, donc 0 dBm : %v", dst)
	}
}

// TestSamplesNePortePasDIfindex — la same property que sur le connecteur RouterOS.
//
// Ce canal n'a pas d'index d'interface a donner. Le controleur exigeant l'tag
// `iftype` pour toucher l'inventaire, ces samples alimentent les courbes sans
// jamais pouvoir modifier la front panel : « la telemetry ne CREATES jamais un port » est
// obtenu par construction.
func TestSamplesNePortePasDIfindex(t *testing.T) {
	var tel = telemetrieDeReference(t)
	var samples = tel.Samples("dev-css")
	if len(samples) == 0 {
		t.Fatal("aucun sample produit")
	}
	var s int
	for s = range samples {
		if samples[s].Labels["device"] != "dev-css" {
			t.Fatalf("sample sans device : %+v", samples[s])
		}
		var _, aIfindex = samples[s].Labels["ifindex"]
		if aIfindex {
			t.Fatalf("sample de scraping porteur d'un index d'interface : %+v",
				samples[s])
		}
	}
}

// TestSamplesNommeLeCapteurDeTemperature
//
// Une serial de temperature SANS tag `sensor` ne pourrait pas cohabiter avec celles
// des gammes qui en ont plusieurs, et il faudrait alors deux requests differentes pour
// tracer la same grandeur sur deux constructeurs. Le capteur est donc named same
// lorsqu'il est seul sur la carte.
func TestSamplesNommeLeCapteurDeTemperature(t *testing.T) {
	var tel = telemetrieDeReference(t)
	var vu bool
	var s metricbuf.Sample
	for _, s = range tel.Samples("dev-css") {
		if s.Name != metricbuf.MetricTemperature {
			continue
		}
		vu = true
		if s.Labels["sensor"] == "" {
			t.Errorf("temperature sans tag `sensor` : %+v", s)
		}
	}
	if !vu {
		t.Error("aucune serial de temperature produite")
	}
}

// TestLesManquesDeLaPlateformeSontDeclaresEnCapability
//
// Platform gaps are declared on Monitoring.Unavailable — not as snapshot notes.
// The UI localizes operator copy from the metric key.
func TestLesManquesDeLaPlateformeSontDeclaresEnCapability(t *testing.T) {
	var mon = (&Driver{}).Capabilities("CSS326-24G-2S+").Monitoring
	var metric string
	for _, metric = range []string{"cpu", "memory"} {
		var _, ok = mon.WhyUnavailable(metric)
		if !ok {
			t.Errorf("%q must be declared unavailable on SwOS capabilities", metric)
		}
	}
}

// TestVitesseTraduiteDepuisLeLibelleEtNonLIndice
//
// La table de SwOS est `"10M 100M 1G 10G 5G 2.5G 40G"` et son ordre n'est PAS croissant.
// Traduire l'indice par une table parallele obligerait a tenir deux listes dans le same
// ordre, ce qui tient jusqu'au firmware qui en ajoute une huitieme au milieu.
func TestVitesseTraduiteDepuisLeLibelleEtNonLIndice(t *testing.T) {
	var i int
	for i = range SpeedLabels {
		var label, _ = SpeedLabel(uint32(i)) //nolint:gosec // indice de table, borne
		var mbps, ok = speedMbps(label)
		if !ok {
			t.Errorf("le label %q de la table de the device n'a pas de traduction "+
				"en Mbit/s : un port negociant ce rate n'aurait aucune serial", label)
			continue
		}
		if mbps <= 0 {
			t.Errorf("%q traduit en %v Mbit/s", label, mbps)
		}
	}
	// L'ordre n'est pas croissant : c'est ce qui interdit une table parallele.
	var troisieme, _ = SpeedLabel(3)
	var quatrieme, _ = SpeedLabel(4)
	var m3, _ = speedMbps(troisieme)
	var m4, _ = speedMbps(quatrieme)
	if m3 <= m4 {
		t.Errorf("la table de rates de SwOS est censee be NON croissante "+
			"(indice 3 = 10G, indice 4 = 5G) : %v puis %v", m3, m4)
	}
}

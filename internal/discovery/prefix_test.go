package discovery

import (
	"strings"
	"testing"
)

func TestExpandPrefixIPv4(t *testing.T) {
	// /30 : 4 adresses, network et diffusion exclus → 2 hotes.
	var hosts, err = ExpandPrefix("192.168.1.0/30")
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 || hosts[0] != "192.168.1.1" || hosts[1] != "192.168.1.2" {
		t.Fatalf("hotes %v", hosts)
	}
}

func TestExpandPrefixSlash24(t *testing.T) {
	var hosts, err = ExpandPrefix("10.20.30.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 254 {
		t.Fatalf("%d hotes, expected 254", len(hosts))
	}
	if hosts[0] != "10.20.30.1" || hosts[253] != "10.20.30.254" {
		t.Fatalf("bornes : %s … %s", hosts[0], hosts[253])
	}
}

// Une adresse non alignee doit be ramenee a son network, sinon le same network
// saisi deux fois differemment produirait deux plages.
func TestExpandPrefixMasksAddress(t *testing.T) {
	var hosts, err = ExpandPrefix("192.168.1.77/30")
	if err != nil {
		t.Fatal(err)
	}
	if hosts[0] != "192.168.1.77" && hosts[0] != "192.168.1.1" {
		t.Logf("premier hote %s", hosts[0])
	}
	var norm string
	norm, err = NormalizePrefix("192.168.1.77/24")
	if err != nil {
		t.Fatal(err)
	}
	if norm != "192.168.1.0/24" {
		t.Fatalf("normalisation : %s", norm)
	}
}

func TestExpandPrefixSingleAddress(t *testing.T) {
	var hosts, err = ExpandPrefix("10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0] != "10.0.0.5" {
		t.Fatalf("%v", hosts)
	}
}

// Un /16 est la plus large plage IPv4 qu'on enumerates : 65 534 hotes, parcourus en
// minutes depuis que les ports sont sondes en parallele.
func TestExpandPrefixSlash16(t *testing.T) {
	var hosts, err = ExpandPrefix("10.0.0.0/16")
	if err != nil {
		t.Fatalf("/16 refused : %v", err)
	}
	if len(hosts) != 65534 {
		t.Fatalf("65534 hotes attendus (network et diffusion exclus), %d obtenus", len(hosts))
	}
}

// Au-dela du /16, on refuse en le disant : ca se decoupe en tranches.
func TestExpandPrefixTooLarge(t *testing.T) {
	var _, err = ExpandPrefix("10.0.0.0/15")
	if err == nil {
		t.Fatal("/15 accepted")
	}
	if !strings.Contains(err.Error(), "plage trop large") {
		t.Fatalf("message : %v", err)
	}
}

// DECLARER et BALAYER sont deux questions distinctes : un /64 IPv6 est le
// sous-network reel d'un site, il doit pouvoir be declared same si son
// enumeration n'a pas de sens.
func TestDeclareVsEnumerate(t *testing.T) {
	var declarable = []string{"10.0.0.0/16", "2001:db8::/64", "192.168.1.10"}
	var cidr string
	for _, cidr = range declarable {
		var err = ValidPrefix(cidr)
		if err != nil {
			t.Errorf("%s devrait be declarable : %v", cidr, err)
		}
	}
	var err = Enumerable("2001:db8::/64")
	if err == nil {
		t.Error("un /64 IPv6 ne doit pas be annonce comme enumerable")
	} else if !strings.Contains(err.Error(), "voisinage") {
		t.Errorf("le refus doit dire par quoi passera la discovery : %v", err)
	}
	err = Enumerable("10.0.0.0/16")
	if err != nil {
		t.Errorf("un /16 IPv4 doit be enumerable : %v", err)
	}
}

// Le refus IPv6 doit expliquer pourquoi, pas seulement refuser.
func TestExpandPrefixIPv6TooWide(t *testing.T) {
	var _, err = ExpandPrefix("2001:db8::/64")
	if err == nil {
		t.Fatal("/64 IPv6 accepted : impossible a enumerate")
	}
	if !strings.Contains(err.Error(), "IPv6") {
		t.Fatalf("message peu explicite : %v", err)
	}
}

func TestExpandPrefixIPv6Narrow(t *testing.T) {
	var hosts, err = ExpandPrefix("2001:db8::/126")
	if err != nil {
		t.Fatal(err)
	}
	// En IPv6 il n'y a pas d'adresse de diffusion : les 4 adresses sont sondees.
	if len(hosts) != 4 {
		t.Fatalf("%d adresses : %v", len(hosts), hosts)
	}
}

func TestExpandPrefixInvalid(t *testing.T) {
	var _, err = ExpandPrefix("pas-une-plage")
	if err == nil {
		t.Fatal("prefix invalide accepted")
	}
}

func TestIdentifyPrefersShellOutput(t *testing.T) {
	// La banniere dit OpenSSH, la sortie de commande dit Cisco : c'est
	// the device lui-same qui doit gagner.
	var m, conf, evidence = Identify(
		"Cisco IOS Software, C2960 Software", "SSH-2.0-OpenSSH_7.4", "", "")
	if m.Driver != "cisco-ios" {
		t.Fatalf("driver %q", m.Driver)
	}
	if conf != ConfShellOutput {
		t.Fatalf("confiance %d", conf)
	}
	if evidence == "" {
		t.Fatal("source de l'identification vide")
	}
}

func TestIdentifyFallsBackToGeneric(t *testing.T) {
	var m, conf, _ = Identify("", "SSH-2.0-OpenSSH_9.6", "", "")
	if m.Driver != "generic-ssh" {
		t.Fatalf("driver %q", m.Driver)
	}
	if conf != ConfPortsOnly {
		t.Fatalf("confiance %d", conf)
	}
}

func TestSearchResetProcedures(t *testing.T) {
	var all = SearchResetProcedures("")
	if len(all) < 10 {
		t.Fatalf("catalogue trop court : %d", len(all))
	}
	var mt = SearchResetProcedures("mikrotik")
	if len(mt) != 1 || mt[0].Driver != "mikrotik-routeros" {
		t.Fatalf("recherche mikrotik : %+v", mt)
	}
	// Recherche par model, pas seulement par constructeur.
	var byModel = SearchResetProcedures("2960")
	if len(byModel) != 1 || byModel[0].Driver != "cisco-ios" {
		t.Fatalf("recherche 2960 : %+v", byModel)
	}
	// Tous les mots doivent porter : « cisco nexus » ne doit pas ramener IOS.
	var multi = SearchResetProcedures("cisco nexus")
	if len(multi) != 1 || multi[0].Driver != "cisco-nxos" {
		t.Fatalf("recherche multi-mots : %+v", multi)
	}
	if len(SearchResetProcedures("marque-inexistante")) != 0 {
		t.Fatal("recherche sans result non vide")
	}
	// Chaque procedure doit dire ce qu'elle efface : sans ca, the UI ment.
	var p ResetProcedure
	for _, p = range all {
		if strings.TrimSpace(p.Erases) == "" {
			t.Fatalf("%s : champ « efface » vide", p.Driver)
		}
		if len(p.Software) == 0 && len(p.Hardware) == 0 {
			t.Fatalf("%s : aucune procedure", p.Driver)
		}
	}
}

// Regression : sur « System Serial Number : FOC1841X0AB », un motif trop
// permissif capture le mot « Number » comme serial number. Un faux serial est
// pire qu'un serial absent : c'est la key de fusion des candidats entre proxys,
// donc deux devices differents finiraient confondus.
func TestExtractFactsRealCiscoBanner(t *testing.T) {
	var banner = `
Cisco IOS Software, C2960X Software (C2960X-UNIVERSALK9-M), Version 15.2(7)E3
hostname sw-atelier-lyon-01
Model Number            : WS-C2960X-24PS-L
System Serial Number    : FOC1841X0AB
`
	var hostname, serial, model = ExtractFacts(banner)
	if hostname != "sw-atelier-lyon-01" {
		t.Fatalf("hostname %q", hostname)
	}
	if serial != "FOC1841X0AB" {
		t.Fatalf("serial %q (le mot « Number » ne doit pas passer)", serial)
	}
	if model != "WS-C2960X-24PS-L" {
		t.Fatalf("model %q", model)
	}
}

func TestExtractFactsOtherVendors(t *testing.T) {
	var cases = []struct {
		text, serial, model string
	}{
		{"Serial number: JN123ABC4567\nModel: ex2300-24t", "JN123ABC4567", "ex2300-24t"},
		{"serial-number: 7C1B08F4E2D9", "7C1B08F4E2D9", ""},
		{"Serial Number      : SG12ABC345\nHardware model : 2530-24G", "SG12ABC345", "2530-24G"},
		// Mot sans chiffre : refused rather que d'inventer un serial.
		{"Serial Number : Unknown", "", ""},
		// Sans separateur, on ne devine pas.
		{"System Serial Number    FOC1841X0AB", "", ""},
	}
	var c struct {
		text, serial, model string
	}
	for _, c = range cases {
		var _, serial, model = ExtractFacts(c.text)
		if serial != c.serial {
			t.Fatalf("%q → serial %q, expected %q", c.text, serial, c.serial)
		}
		if model != c.model {
			t.Fatalf("%q → model %q, expected %q", c.text, model, c.model)
		}
	}
}

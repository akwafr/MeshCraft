package main

import (
	"testing"

	"meshcraft/internal/tunnel"
)

// Ce que ce fichier tests : ce que le cliche de reannonce DIT, et surtout ce qu'il
// TAIT.
//
// Le cliche existe parce qu'une transition perdue ne se rejouait jamais (cf.
// tunnel.Event.Resync). Mais reannoncer trop serait pire que de ne rien reannoncer :
// une cible jamais pingee presented comme « en ligne » ferait write au controleur un
// constat que personne n'a fait, et sur une plateforme sans console de secours c'est
// le genre de statut qui fait renoncer a un deplacement.

// TestSnapshotSilentWhenUnobserved : une cible sans avis ne produit rien.
//
// `up == nil` veut dire « pas encore pingee ». Ni en ligne, ni injoignable : il n'y a
// aucun constat a reannoncer, et en inventer un serait affirmer plus que ce que la
// surveillance sait.
func TestSnapshotSilentWhenUnobserved(t *testing.T) {
	var m = NewMonitor()
	m.SetConfig(tunnel.WatchConfig{Targets: []tunnel.WatchTarget{
		{DeviceID: "dev-jamais", IP: "10.0.0.1", Name: "never-pinged"},
	}})

	var snap = m.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("cliche = %d evenement(s), expected 0 : une cible sans avis ne se "+
			"reannonce pas", len(snap))
	}
}

// TestSnapshotReturnsCurrentStateInOrder : un evenement par cible ayant un avis,
// trie par adresse, marque `Resync`.
//
// L'ordre stable n'est pas une coquetterie : deux cliches du same etat doivent
// produire la same suite d'octets, sinon comparer deux flux de reconnexion ne veut
// plus rien dire — et un parcours de carte Go varie a chaque execution.
func TestSnapshotReturnsCurrentStateInOrder(t *testing.T) {
	var m = NewMonitor()
	m.SetConfig(tunnel.WatchConfig{Targets: []tunnel.WatchTarget{
		{DeviceID: "dev-c", IP: "10.0.0.30", Name: "c"},
		{DeviceID: "dev-a", IP: "10.0.0.10", Name: "a"},
		{DeviceID: "dev-b", IP: "10.0.0.20", Name: "b"},
		// Sans DeviceID : le controleur n'aurait rien a en faire.
		{DeviceID: "", IP: "10.0.0.40", Name: "anonyme"},
	}})

	var oui = true
	var non = false
	m.states["10.0.0.10"].up = &oui
	m.states["10.0.0.20"].up = &non
	m.states["10.0.0.20"].misses = 7
	m.states["10.0.0.30"].up = &oui
	m.states["10.0.0.40"].up = &oui

	var snap = m.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("cliche = %d evenement(s), expected 3 (l'anonyme est passe)", len(snap))
	}

	var expected = []struct {
		ip     string
		typ    string
		misses int
	}{
		{"10.0.0.10", tunnel.EventDeviceUp, 0},
		{"10.0.0.20", tunnel.EventDeviceDown, 7},
		{"10.0.0.30", tunnel.EventDeviceUp, 0},
	}
	var i int
	for i = range expected {
		if snap[i].IP != expected[i].ip {
			t.Errorf("rang %d : adresse %s, expected %s — le cliche doit be trie",
				i+1, snap[i].IP, expected[i].ip)
		}
		if snap[i].Type != expected[i].typ {
			t.Errorf("%s : type %s, expected %s", snap[i].IP, snap[i].Type, expected[i].typ)
		}
		if snap[i].Misses != expected[i].misses {
			t.Errorf("%s : misses %d, expected %d", snap[i].IP, snap[i].Misses,
				expected[i].misses)
		}
		if !snap[i].Resync {
			t.Errorf("%s : non marque Resync — le controleur le prendrait pour une "+
				"bascule fraiche et journaliserait une panne qui n'a pas eu lieu",
				snap[i].IP)
		}
		if snap[i].TS.IsZero() {
			t.Errorf("%s : horodatage vide", snap[i].IP)
		}
	}
}

// TestSnapshotDropsRemovedTarget : une cible retiree de la consigne disparait du
// cliche.
//
// `SetConfig` oublie les cibles absentes de la nouvelle consigne, et le cliche doit
// suivre : reannoncer state d'un device qu'on ne surveille plus ferait write un
// statut que rien ne rafraichira jamais.
func TestSnapshotDropsRemovedTarget(t *testing.T) {
	var m = NewMonitor()
	m.SetConfig(tunnel.WatchConfig{Targets: []tunnel.WatchTarget{
		{DeviceID: "dev-a", IP: "10.0.0.10"},
		{DeviceID: "dev-b", IP: "10.0.0.20"},
	}})
	var oui = true
	m.states["10.0.0.10"].up = &oui
	m.states["10.0.0.20"].up = &oui

	m.SetConfig(tunnel.WatchConfig{Targets: []tunnel.WatchTarget{
		{DeviceID: "dev-a", IP: "10.0.0.10"},
	}})

	var snap = m.Snapshot()
	if len(snap) != 1 || snap[0].DeviceID != "dev-a" {
		t.Fatalf("cliche = %+v, expected le seul dev-a", snap)
	}
}

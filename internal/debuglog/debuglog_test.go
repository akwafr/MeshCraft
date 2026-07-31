package debuglog

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRingBorneLaMemoire(t *testing.T) {
	var r = NewRing(3)
	var i int
	for i = 1; i <= 10; i++ {
		r.Add(Line{TS: time.Now(), Level: "INFO", Msg: "ligne"})
	}
	var got = r.Snapshot("")
	if len(got) != 3 {
		t.Fatalf("l'anneau doit borner a 3 lignes, %d conservees", len(got))
	}
	// De la plus ancienne a la plus recente : un journal qui remonte le temps ne
	// se lit pas.
	if got[0].Seq != 8 || got[2].Seq != 10 {
		t.Errorf("ordre inattendu : %d → %d", got[0].Seq, got[2].Seq)
	}
}

func TestSnapshotFiltreParNiveau(t *testing.T) {
	var r = NewRing(10)
	r.Add(Line{TS: time.Now(), Level: "INFO", Msg: "a"})
	r.Add(Line{TS: time.Now(), Level: "ERROR", Msg: "b"})
	var got = r.Snapshot("WARN")
	if len(got) != 1 || got[0].Msg != "b" {
		t.Errorf("filtre par niveau casse : %+v", got)
	}
}

// TestHandlerMasqueLesSecrets : ce flux se consulte dans un navigateur. Un mot
// de passe qui y transite est un mot de passe fuite, et le filtrage porte sur le
// NOM de l'attribut — chercher un secret dans une valeur supposerait de savoir a
// quoi il ressemble, ce qui est faux par construction.
func TestHandlerMasqueLesSecrets(t *testing.T) {
	var r = NewRing(10)
	var h = NewHandler(slog.NewJSONHandler(discard{}, nil), r)
	var log = slog.New(h)

	log.Info("connexion", "user", "mcadmin", "password", "SuperSecret1",
		"secret_key", "abc", "api_token", "xyz", "device", "sw-labo")

	var got = r.Snapshot("")
	if len(got) != 1 {
		t.Fatalf("1 ligne attendue, %d", len(got))
	}
	var a = got[0].Attrs
	if a["password"] != "[masque]" || a["secret_key"] != "[masque]" ||
		a["api_token"] != "[masque]" {
		t.Errorf("un secret a fuite : %+v", a)
	}
	// Ce qui n'est pas sensible doit rester lisible, sinon le journal ne sert a
	// rien.
	if a["device"] != "sw-labo" || a["user"] != "mcadmin" {
		t.Errorf("les attributs utiles doivent rester : %+v", a)
	}
}

// TestHandlerNeDetournePasLeFlux : les logs doivent continuer d'aller sur la
// sortie standard. Une zone de debug qui capterait le flux ferait perdre les
// journaux du conteneur au moment where on en a le plus besoin.
func TestHandlerNeDetournePasLeFlux(t *testing.T) {
	var out = &capture{}
	var r = NewRing(10)
	slog.New(NewHandler(slog.NewJSONHandler(out, nil), r)).Warn("attention")
	if !strings.Contains(out.String(), "attention") {
		t.Errorf("le handler suivant doit toujours recevoir la ligne : %q", out.String())
	}
	if len(r.Snapshot("")) != 1 {
		t.Error("et l'anneau aussi")
	}
}

// TestSubscribeEstBorne : chaque flux tient un canal et une goroutine HTTP. Un
// onglet oublie ne doit pas couter indefiniment.
func TestSubscribeEstBorne(t *testing.T) {
	var r = NewRing(10)
	r.maxSubs = 2
	var _, stop1, ok1 = r.Subscribe()
	var _, stop2, ok2 = r.Subscribe()
	var _, _, ok3 = r.Subscribe()
	if !ok1 || !ok2 {
		t.Fatal("les deux premiers abonnements doivent passer")
	}
	if ok3 {
		t.Error("au-dela de la borne, l'abonnement doit be refused")
	}
	stop1()
	stop2()
	var _, stop4, ok4 = r.Subscribe()
	if !ok4 {
		t.Error("liberer un abonnement doit rendre la place")
	}
	stop4()
}

// TestAddNeBloquePasSurAbonneLent : un navigateur sur un lien mediocre ne doit
// jamais ralentir le code qui journalise. Il perd des lignes, c'est le bon
// compromis pour du debug.
func TestAddNeBloquePasSurAbonneLent(t *testing.T) {
	var r = NewRing(10)
	var _, stop, _ = r.Subscribe()
	defer stop()
	var done = make(chan struct{})
	go func() {
		var i int
		for i = 0; i < 1000; i++ {
			r.Add(Line{TS: time.Now(), Level: "INFO", Msg: "flood"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Add bloque quand un abonne ne consomme pas")
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

type capture struct{ b strings.Builder }

func (c *capture) Write(p []byte) (int, error) { return c.b.Write(p) }
func (c *capture) String() string              { return c.b.String() }

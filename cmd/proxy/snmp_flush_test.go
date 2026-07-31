package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"meshcraft/internal/metricbuf"
)

// Ce que ces tests protegent : que le tampon AVANCE.
//
// Le tampon existe pour qu'une coupure de tunnel ne perde pas les minutes qu'elle
// dure, et pour ca `flush` rend au tampon tout lot qui n'est pas parti. Mais
// `metricbuf.Take` rend TOUJOURS le same lot reserve : rendre un lot que le
// controleur refusera encore, c'est le rejouer a l'identique pour toujours. Le
// tampon ne se vide plus, il deborde par le haut, et ce sont les points les plus
// RECENTS que la collecte perd — l'arret complet de la remontee pour ce proxy, et
// non une perte partielle.
//
// Hence trois properties, et la first est celle qui donne son sens aux autres :
//
//  1. sur un refus DEFINITIF, le lot est abandonne et le lot suivant est DIFFERENT ;
//  2. sur un failure PASSAGER, le lot suivant est le SAME — c'est la raison d'be du
//     tampon, et la corriger ne doit pas l'emporter ;
//  3. un abandon dit combien de points il sacrifie. Un abandon silencieux serait
//     pire que la boucle qu'il remplace : la boucle, elle, finit par se voir.
//
// Sans network reel : `flush` prend son client HTTP dans le collecteur, un serveur de
// test suffit donc a jouer le controleur — y compris pour ses responses les plus
// difficiles a obtenir en vrai, qui sont justement celles qui bloquaient.

// lotRecu : ce qui identifie un lot vu du controleur.
//
// Ses BORNES, pas ses samples. Toute la question applied a ces tests est « le lot
// suivant est-il le same que le previous ? », et deux valeurs y repondent — a
// condition que les samples soient numerotes, ce dont echantillonsNumerotes se
// charge.
type lotRecu struct {
	n       int
	premier float64
	dernier float64
}

// receveur enregistre les lots que le faux controleur receives.
//
// Le verrou n'est pas decoratif : le gestionnaire HTTP tourne dans la goroutine du
// serveur de test, la lecture dans celle du test. L'aller-retour par la connexion
// suffirait probablement a ordonner les deux, mais « probablement » n'est pas ce
// qu'on demande a un test lance avec -race.
type receveur struct {
	mu   sync.Mutex
	lots []lotRecu
}

func (rc *receveur) ajoute(t *testing.T, r *http.Request) {
	var in struct {
		ProxyID string             `json:"proxy_id"`
		Samples []metricbuf.Sample `json:"samples"`
	}
	// t.Errorf et non t.Fatalf : on est dans la goroutine du serveur, where stop le
	// test n'est pas permis.
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		t.Errorf("corps illisible cote controleur")
		return
	}
	if in.ProxyID != tProxyID {
		t.Errorf("proxy_id expected %q, recu %q", tProxyID, in.ProxyID)
	}
	if len(in.Samples) == 0 {
		t.Errorf("lot vide recu : flush ne doit pas envoyer de request pour rien")
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.lots = append(rc.lots, lotRecu{
		n: len(in.Samples), premier: in.Samples[0].Value,
		dernier: in.Samples[len(in.Samples)-1].Value,
	})
}

func (rc *receveur) vus() []lotRecu {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]lotRecu{}, rc.lots...)
}

// echantillonsNumerotes rend `n` samples dont la VALEUR est le rang. C'est ce
// number qui rend « le same lot » et « le lot suivant » distinguables ; sans lui, un
// rejeu et un avancement produisent deux corps identiques et le test ne prouve rien.
func echantillonsNumerotes(n int) []metricbuf.Sample {
	var out = make([]metricbuf.Sample, 0, n)
	var i int
	for i = 0; i < n; i++ {
		out = append(out, metricbuf.Sample{
			Name:   "meshcraft_if_in_octets_total",
			Labels: map[string]string{"device": tDeviceID, "ifindex": strconv.Itoa(i)},
			Value:  float64(i),
		})
	}
	return out
}

// controleurQuiRepond monte un faux controleur qui enregistre puis repond `status`
// avec `corps`, et le collecteur qui lui parle.
//
// Le tampon est dimensionne EXACTEMENT a ce qu'on y met : ainsi tout `Dropped` non
// nul dans un de ces tests est un vrai symptome, jamais un effet du montage.
func controleurQuiRepond(t *testing.T, status int, corps string,
	enTetes map[string]string, n int) (*SNMPCollector, *receveur) {
	t.Helper()
	var rc = &receveur{}
	var srv = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/proxy/metrics" {
				t.Errorf("chemin inattendu : %s", r.URL.Path)
			}
			rc.ajoute(t, r)
			var k, v string
			for k, v = range enTetes {
				w.Header().Set(k, v)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			var _, err = w.Write([]byte(corps))
			if err != nil {
				t.Errorf("write de la response : %v", err)
			}
		}))
	t.Cleanup(srv.Close)
	var c = &SNMPCollector{
		buf:           metricbuf.New(n),
		controllerURL: srv.URL,
		proxyID:       tProxyID,
		secret:        tSecret,
		client:        srv.Client(),
	}
	c.buf.AddAll(echantillonsNumerotes(n))
	return c, rc
}

// refus413 : le corps que le controleur renvoie vraiment sur un lot trop volumineux
// (cf. failJSON et batchGeste, internal/api). Recopie parce que c'est le motif que
// l'exploitant lira dans le journal du proxy : si sa forme change, c'est le message
// d'abandon qui perd son interet.
const refus413 = `{"error":"corps de request trop grand : plafond de 8388608 ` +
	`octets ; le lot du proxy est trop volumineux, reduisez maxPerFlush ` +
	`(ce n'est pas un probleme de contenu)"}`

// TestRefusDefinitifAbandonneEtLeTamponAvance est LE test du lot.
//
// Il tests la seule property qui distingue la reparation du default : after un
// refus definitif, le lot SUIVANT n'est pas le previous. Tout le reste — le
// journal, la frontiere, les chiffres — n'a d'interet que si celle-ci tient.
func TestRefusDefinitifAbandonneEtLeTamponAvance(t *testing.T) {
	// Plus d'un lot dans le tampon, sinon « le lot suivant est different » ne veut
	// rien dire : c'est exactement la property qu'on mesure.
	var enTampon = maxPerFlush + 500
	var c, rc = controleurQuiRepond(t, http.StatusRequestEntityTooLarge, refus413,
		nil, enTampon)
	var ctx = context.Background()

	// Faux same sur un abandon, alors que le tampon a pourtant avance : un refus
	// systematique sacrifierait quatre lots par wake au lieu d'un si la rafale
	// continuait.
	if c.flush(ctx) {
		t.Errorf("flush doit rendre faux sur un refus : la rafale stops there")
	}
	if c.flush(ctx) {
		t.Errorf("flush doit rendre faux sur un refus : la rafale stops there")
	}

	var lots = rc.vus()
	if len(lots) != 2 {
		t.Fatalf("2 lots attendus au controleur, recu %d", len(lots))
	}
	if lots[0].premier == lots[1].premier {
		t.Fatalf("le tampon N'A PAS AVANCE : les deux lots commencent au point %.0f "+
			"— c'est la boucle infinie que ce lot repare", lots[0].premier)
	}
	if lots[0].n != maxPerFlush || lots[0].premier != 0 {
		t.Errorf("premier lot : %d samples a partir de %.0f, expected %d a partir "+
			"de 0", lots[0].n, lots[0].premier, maxPerFlush)
	}
	if lots[1].premier != float64(maxPerFlush) || lots[1].n != 500 {
		t.Errorf("second lot : %d samples a partir de %.0f, expected 500 a partir "+
			"de %d", lots[1].n, lots[1].premier, maxPerFlush)
	}

	// Le tampon est vide, et surtout : il n'a plus rien de RESERVE. Un `Pending`
	// residuel serait le same blocage sous un autre nom, puisque `Take` le rendrait
	// encore.
	var st = c.buf.Stats()
	if st.Queued != 0 || st.Pending != 0 {
		t.Errorf("tampon expected vide, obtenu %d en attente et %d reserves",
			st.Queued, st.Pending)
	}
	// `Dropped` compte les pertes par SATURATION, pas les abandons : un lot abandonne
	// ne laisse aucune trace dans les statistiques du tampon. C'est precisely
	// pourquoi l'abandon doit se journaliser lui-same (voir le test suivant).
	if st.Dropped != 0 {
		t.Errorf("aucune perte par saturation attendue, obtenu %d", st.Dropped)
	}
}

// TestEchecPassagerRejoueLeMemeLot est la non-regression du tampon, et elle compte
// autant que le test previous : distinguer le refus definitif ne doit pas
// transformer une panne de controleur en perte de mesures. C'est ce que le tampon
// existe pour empecher.
func TestEchecPassagerRejoueLeMemeLot(t *testing.T) {
	// 503 : la response d'un controleur sans stockage de metriques configure. Il la
	// choisit EXPRESSEMENT pour que le proxy garde son lot (cf. handleProxyMetrics).
	var c, rc = controleurQuiRepond(t, http.StatusServiceUnavailable,
		`{"error":"stockage de metriques non configure sur ce controleur"}`,
		nil, maxPerFlush+500)
	var ctx = context.Background()
	var i int
	for i = 0; i < 3; i++ {
		if c.flush(ctx) {
			t.Errorf("flush doit rendre faux sur un refus (tentative %d)", i+1)
		}
	}
	var lots = rc.vus()
	if len(lots) != 3 {
		t.Fatalf("3 tentatives attendues, recu %d", len(lots))
	}
	for i = 1; i < len(lots); i++ {
		if lots[i] != lots[0] {
			t.Fatalf("le lot %d differe du premier (%+v vs %+v) : un failure passager "+
				"doit rejouer le SAME lot, sans quoi une panne de controleur devient "+
				"une perte de mesures", i+1, lots[i], lots[0])
		}
	}
	// Rien n'est perdu, et rien ne reste RESERVE : `Nack` remet le lot dans la file, EN
	// TETE, et libere la reservation. C'est ce qui explique le rejeu a l'identique
	// mieux que « Take rend le lot reserve » — le lot suivant est le same parce qu'il
	// est repris au same endroit de la file, pas parce qu'une reservation survit.
	var st = c.buf.Stats()
	if st.Queued != maxPerFlush+500 || st.Pending != 0 || st.Dropped != 0 {
		t.Errorf("expected %d en attente / 0 reserve / 0 perdu, obtenu %d / %d / %d",
			maxPerFlush+500, st.Queued, st.Pending, st.Dropped)
	}
}

// TestUnAbandonDitCombienDePointsIlSacrifie : le nombre de points sacrifies n'a pas
// d'autre endroit where se dire.
//
// `metricbuf` ne le compte pas — un abandon n'est pas une saturation — donc si cette
// ligne ne le porte pas, la mesure disparait sans laisser de trace, et un abandon
// silencieux est pire que la boucle qu'il remplace. Le motif du controleur y figure
// aussi : c'est lui qui dit lequel des reglages du proxy est en cause.
func TestUnAbandonDitCombienDePointsIlSacrifie(t *testing.T) {
	var journal bytes.Buffer
	var precedent = slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&journal,
		&slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(precedent)

	// Sept points : un compte qu'on reconnait a l'œil dans la ligne, et un lot
	// INCOMPLET — l'abandon ne depend pas de la taille du lot.
	var c, _ = controleurQuiRepond(t, http.StatusRequestEntityTooLarge, refus413,
		nil, 7)
	c.flush(context.Background())

	var ligne = journal.String()
	t.Logf("journal : %s", strings.TrimSpace(ligne))
	if !strings.Contains(ligne, "abandonnes=7") {
		t.Errorf("le nombre de points sacrifies doit figurer au journal (abandonnes=7), "+
			"obtenu : %s", ligne)
	}
	if !strings.Contains(ligne, "status=413") {
		t.Errorf("le code de refus doit figurer au journal, obtenu : %s", ligne)
	}
	if !strings.Contains(ligne, "reduisez maxPerFlush") {
		t.Errorf("le motif du controleur doit figurer au journal, obtenu : %s", ligne)
	}
	if !strings.Contains(ligne, "level=WARN") {
		t.Errorf("un abandon de mesures est un WARN, obtenu : %s", ligne)
	}

	// Et un failure PASSAGER ne doit pas se dire « abandonnees » : c'est la same ligne a
	// un mot pres, et ce mot est toute la difference pour celui qui la lit.
	journal.Reset()
	var passager, _ = controleurQuiRepond(t, http.StatusBadGateway,
		`{"error":"victoriametrics: connexion refused"}`, nil, 7)
	passager.flush(context.Background())
	ligne = journal.String()
	if strings.Contains(ligne, "abandonn") {
		t.Errorf("un 502 conserve le lot : la ligne ne doit pas parler d'abandon, "+
			"obtenu : %s", ligne)
	}
	if !strings.Contains(ligne, "conservees en tampon") {
		t.Errorf("un 502 doit dire que le lot est conserve, obtenu : %s", ligne)
	}
}

// TestUnRetryAfterEmpecheLAbandon : le cas qu'un `>= 400` un peu large rangerait du
// mauvais cote. Eprouve de bout en bout et pas seulement sur refusDefinitif, parce
// que ce qui compte est que `flush` LISE cet en-tete.
func TestUnRetryAfterEmpecheLAbandon(t *testing.T) {
	// Un 413 accompagne d'un « reviens dans 10 s » ne vient pas du garde-fou de lot :
	// il vient d'un intermediaire qui se protege, et il demande explicitement un
	// rejeu.
	var c, rc = controleurQuiRepond(t, http.StatusRequestEntityTooLarge,
		`{"error":"reviens plus tard"}`, map[string]string{"Retry-After": "10"}, 7)
	var ctx = context.Background()
	c.flush(ctx)
	c.flush(ctx)
	var lots = rc.vus()
	if len(lots) != 2 {
		t.Fatalf("2 tentatives attendues, recu %d", len(lots))
	}
	if lots[0] != lots[1] {
		t.Errorf("un refus qui dit quand revenir doit be rejoue tel quel : %+v puis %+v",
			lots[0], lots[1])
	}
	// Rendus a la file, pas abandonnes : les sept points sont toujours there.
	var st = c.buf.Stats()
	if st.Queued != 7 || st.Pending != 0 {
		t.Errorf("les 7 points doivent revenir dans la file, obtenu %d en attente et "+
			"%d reserves", st.Queued, st.Pending)
	}
}

// TestFrontiereDuRefusDefinitif written la frontiere cas par cas, avec le POURQUOI de
// chaque cote.
//
// C'est la table qui vaudra, dans six mois, mieux qu'une relecture du `switch` : elle
// dit ce qu'on a decide, et pour quelle raison. Les deux cas a defendre sont 401/403
// — passagers, un secret se rotationne et une revocation s'annule, alors
// qu'abandonner ne remonterait pas un point de plus — et 429 avec ou sans
// `Retry-After`, qui est une demande de patience et pas un desaccord sur le lot.
func TestFrontiereDuRefusDefinitif(t *testing.T) {
	var cas = []struct {
		status     int
		retryAfter string
		definitif  bool
		pourquoi   string
	}{
		{http.StatusBadRequest, "", true,
			"400 : le corps a been lu et refused, les memes octets rendront le same verdict"},
		{http.StatusRequestEntityTooLarge, "", true,
			"413 : le contenu est bon, c'est le volume qui ne passe pas — et Take ne " +
				"sait pas rendre un lot plus petit"},
		{http.StatusUnsupportedMediaType, "", true,
			"415 : flush envoie toujours le same Content-Type"},
		{http.StatusUnprocessableEntity, "", true,
			"422 : le controleur a compris la request et refuse le fond"},
		{http.StatusUnauthorized, "", false,
			"401 : le lot n'est same pas en cause, l'authentification passe after la " +
				"lecture du corps ; un secret se rotationne"},
		{http.StatusForbidden, "", false,
			"403 : une revocation s'annule, et abandonner ne remonterait pas un point " +
				"de plus — ca n'en effacerait que la trace"},
		{http.StatusNotFound, "", false,
			"404 : route absente, controleur d'une version anterieure ou URL mal reglee"},
		{http.StatusMethodNotAllowed, "", false, "405 : idem 404, cote method"},
		{http.StatusRequestTimeout, "", false, "408 : un delai d'attente est a refaire"},
		{http.StatusConflict, "", false,
			"409 : cette route n'en produit pas — on ne classe pas definitif ce qu'on " +
				"ne connait pas"},
		{http.StatusTooManyRequests, "", false,
			"429 : une demande de patience, pas un desaccord sur le lot"},
		{http.StatusTooManyRequests, "5", false, "429 avec le delai annonce"},
		{http.StatusInternalServerError, "", false, "500 : le controleur est en difficulte"},
		{http.StatusBadGateway, "", false,
			"502 : write VictoriaMetrics en failure, response choisie POUR que le " +
				"proxy garde son lot"},
		{http.StatusServiceUnavailable, "", false,
			"503 : stockage non configure, same intention"},
		{http.StatusServiceUnavailable, "30", false, "503 de maintenance annoncee"},
		{http.StatusGatewayTimeout, "", false, "504 : le controleur n'a pas repondu a temps"},
		{http.StatusNotImplemented, "", false,
			"501 : un controleur anterieur, qu'un deploiement repare — aucun 5xx n'est " +
				"definitif"},
		{http.StatusRequestEntityTooLarge, "10", false,
			"413 + Retry-After : l'en-tete est prioritaire, c'est lui qui empeche un " +
				">= 400 trop large de confondre patience et refus"},
		{http.StatusBadRequest, "1", false,
			"400 + Retry-After : same rule, et elle ne souffre pas d'exception — un " +
				"refus qui dit quand revenir attend qu'on revienne"},
	}
	var i int
	for i = range cas {
		var got = refusDefinitif(cas[i].status, cas[i].retryAfter)
		if got != cas[i].definitif {
			t.Errorf("status %d (Retry-After %q) : definitif=%v expected %v — %s",
				cas[i].status, cas[i].retryAfter, got, cas[i].definitif, cas[i].pourquoi)
		}
	}
}

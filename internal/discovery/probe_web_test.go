package discovery

import (
	"context"
	"crypto/md5" //nolint:gosec // impose par Digest, cf. internal/driver/webfetch.go
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// Ces tests rejouent les TROIS issues de l'essai d'identifiants sur une interface
// web, parce que ce sont trois gestes differents pour l'exploitant : corriger un
// mot de passe, write un connecteur, ou ne rien faire du tout. Un test qui ne
// verifierait que « ca a marche / ca n'a pas marche » laisserait passer
// exactement le default qu'on corrige.
//
// Les devices simules reproduisent le comportement observe sur le parc de
// test : la RACINE repond 200 a un anonyme dans les trois cas. C'est ce qui rend
// la distinction difficile, et c'est pour ca qu'elle est testee.

// webDialer envoie les connexions vers l'adresse du serveur de test : c'est la
// place du tunnel de proxy dans la vraie sonde.
//
// `open` filtre les ports accepted, pour que le balayage de ports d'un test de
// bout en bout voie un device credible — un faux device dont les six
// ports repondent ferait tenter un SSH sur un serveur HTTP.
type webDialer struct {
	addr string
	open map[int]bool
}

func (d webDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if d.open != nil {
		var _, portStr, splitErr = net.SplitHostPort(addr)
		if splitErr != nil {
			return nil, splitErr
		}
		var port, convErr = strconv.Atoi(portStr)
		if convErr != nil {
			return nil, convErr
		}
		if !d.open[port] {
			return nil, errors.New("port closed (device de test)")
		}
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, d.addr)
}

// proberTo construit une sonde dont les connexions vont au serveur de test.
func proberTo(srv *httptest.Server) *Prober {
	return &Prober{Dialer: webDialer{addr: strings.TrimPrefix(srv.URL, "http://")}}
}

// authParams decoupe un en-tete Authorization Digest en paires key/valeur.
func authParams(header string) map[string]string {
	var out = map[string]string{}
	var space = strings.IndexByte(header, ' ')
	if space < 0 {
		return out
	}
	var field string
	for _, field = range strings.Split(header[space+1:], ",") {
		var eq = strings.IndexByte(field, '=')
		if eq < 0 {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(field[:eq]))] =
			strings.Trim(strings.TrimSpace(field[eq+1:]), `"`)
	}
	return out
}

func md5sum(s string) string {
	var sum = md5.Sum([]byte(s)) //nolint:gosec
	return hex.EncodeToString(sum[:])
}

// digestOK verifies la response Digest comme le ferait le firmware : en la
// RECALCULANT. Sans ce calcul, le test validerait la forme de l'en-tete et non
// le fait que the device l'accepterait.
func digestOK(header, method, realm, user, password string) bool {
	if !strings.HasPrefix(strings.ToLower(header), "digest ") {
		return false
	}
	var p = authParams(header)
	if p["username"] != user || p["realm"] != realm {
		return false
	}
	var ha1 = md5sum(user + ":" + realm + ":" + password)
	var ha2 = md5sum(method + ":" + p["uri"])
	var want string
	if p["qop"] == "auth" {
		want = md5sum(ha1 + ":" + p["nonce"] + ":" + p["nc"] + ":" + p["cnonce"] + ":auth:" + ha2)
	} else {
		want = md5sum(ha1 + ":" + p["nonce"] + ":" + ha2)
	}
	return p["response"] == want
}

// --- Issue 1a : Digest accepted (le cas du MikroTik SwOS du parc de test) ---

func TestTryWebDigestAccepted(t *testing.T) {
	const realm = "CSS326-24G-2S+"
	// Mot de passe VIDE : c'est le reglage d'usine de ce switch, et un essai qui
	// exigerait un secret non vide ne l'atteindrait jamais.
	var mux = http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><title>MikroTik SwOS</title></html>"))
	})
	mux.HandleFunc("/sys.b", func(w http.ResponseWriter, r *http.Request) {
		if !digestOK(r.Header.Get("Authorization"), r.Method, realm, "admin", "") {
			w.Header().Set("WWW-Authenticate",
				`Digest realm="`+realm+`", qop="auth", nonce="244ead9d", stale=FALSE`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Corps observed sur the device reel : des champs COURTS et hexadecimaux.
		// `brd` porte bien « CSS326-24G-2S+ », mais encoded.
		_, _ = w.Write([]byte("{upt:0x00e55b73,mac:'b869f45f14a8'," +
			"brd:'4353533332362d3234472d32532b',ver:'322e3137'}"))
	})
	var srv = httptest.NewServer(mux)
	defer srv.Close()

	var att = proberTo(srv).tryWeb(context.Background(), "10.0.0.36", []int{80},
		[]Credential{{ID: "c1", Name: "admin d'usine MikroTik", Kind: "http",
			Username: "admin", Secret: ""}},
		Matcher{Driver: "mikrotik-swos"})

	if att.outcome != WebAuthOK {
		t.Fatalf("issue = %q (detail : %s), expected %q", att.outcome, att.detail, WebAuthOK)
	}
	if att.cred.ID != "c1" || att.cred.Name != "admin d'usine MikroTik" {
		t.Errorf("identifiant retenu = %q/%q", att.cred.ID, att.cred.Name)
	}
	if !strings.Contains(att.detail, "Digest") || !strings.Contains(att.detail, realm) {
		t.Errorf("le detail doit nommer le schema et le realm : %q", att.detail)
	}
	if !strings.Contains(att.path, "/sys.b") {
		t.Errorf("chemin retenu = %q, expected la page de data et non la root", att.path)
	}
	// Ce que la sonde generique ne doit PAS faire : inventer des faits. Les champs
	// de cette page sont hexadecimaux, et les decode est du savoir de plateforme
	// — le connecteur swos le fait, pas la sonde. Un model bricole ici vaudrait
	// moins que rien : il donnerait a l'identification une assurance fausse.
	var host, serial, model = ExtractFacts(att.body)
	if host != "" || serial != "" || model != "" {
		t.Errorf("faits inventes depuis une page qu'on ne sait pas lire : %q/%q/%q",
			host, serial, model)
	}
}

// --- Issue 1b : Basic accepted (le cas de l'API REST de RouterOS) ---

func TestTryWebBasicAccepted(t *testing.T) {
	var mux = http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><title>RouterOS</title></html>"))
	})
	mux.HandleFunc("/rest/system/resource", func(w http.ResponseWriter, r *http.Request) {
		var want = "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:s3cret"))
		if r.Header.Get("Authorization") != want {
			// RouterOS annonce « Basic » tout court, sans realm : le cas est reel et
			// il ne doit pas empecher de se present .
			w.Header().Set("WWW-Authenticate", "Basic")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"platform":"MikroTik","board-name":"CRS310-1G-5S-4S+",` +
			`"version":"7.16.2 (stable)"}`))
	})
	var srv = httptest.NewServer(mux)
	defer srv.Close()

	// Genre VIDE : l'identifiant ponctuel saisi dans the UI d'ajout par IP. Il
	// doit be essaye sur le web, sinon une gamme web-only reste inaccessible
	// same quand l'exploitant tape le bon mot de passe.
	var att = proberTo(srv).tryWeb(context.Background(), "10.0.0.54", []int{80},
		[]Credential{{Name: "saisi manuellement", Kind: "",
			Username: "admin", Secret: "s3cret"}},
		Matcher{Driver: "mikrotik-routeros"})

	if att.outcome != WebAuthOK {
		t.Fatalf("issue = %q (detail : %s), expected %q", att.outcome, att.detail, WebAuthOK)
	}
	if !strings.Contains(att.detail, "Basic") {
		t.Errorf("le detail doit nommer le schema annonce : %q", att.detail)
	}
	if identifyFromWeb(att.body).Driver != "mikrotik-routeros" {
		t.Errorf("la page authentifiee doit suffire a reconnaitre la plateforme")
	}
}

// --- Issue 2 : identifiants refused ---

func TestTryWebRefused(t *testing.T) {
	var attempts int
	var mux = http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><title>MikroTik SwOS</title></html>"))
	})
	mux.HandleFunc("/sys.b", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			attempts++
		}
		w.Header().Set("WWW-Authenticate", `Digest realm="CSS326-24G-2S+", qop="auth", nonce="abc"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	var srv = httptest.NewServer(mux)
	defer srv.Close()

	var att = proberTo(srv).tryWeb(context.Background(), "10.0.0.36", []int{80},
		[]Credential{
			{ID: "c1", Name: "un", Kind: "http", Username: "admin", Secret: "faux1"},
			{ID: "c2", Name: "deux", Kind: "http", Username: "admin", Secret: "faux2"},
			{ID: "c3", Name: "trois", Kind: "http", Username: "admin", Secret: "faux3"},
		},
		Matcher{Driver: "mikrotik-swos"})

	if att.outcome != WebAuthRefused {
		t.Fatalf("issue = %q (detail : %s), expected %q", att.outcome, att.detail, WebAuthRefused)
	}
	if att.cred.ID != "" || att.cred.Name != "" {
		t.Errorf("aucun identifiant ne doit be credite d'un refus : %q/%q",
			att.cred.ID, att.cred.Name)
	}
	// La borne anti-verrouillage porte AUSSI sur le web : la politique AAA de
	// the device compte les echecs toutes origines confondues. Sans elle, un
	// balayage avec dix identifiants enregistres verrouillerait le compte de
	// l'equipe network sur chaque device rencontre.
	if attempts != maxAuthFailures {
		t.Errorf("%d essais authentifies, expected %d (borne anti-verrouillage)",
			attempts, maxAuthFailures)
	}
}

// --- Issue 3 : cette interface n'authentifie pas au niveau HTTP ---

func TestTryWebNoHTTPAuth(t *testing.T) {
	// UI a formulaire, a la way d'un Aruba Instant On : 200 a l'anonyme, session
	// applied dans un cookie after un POST dont seul un connecteur connait la forme.
	var sawAuthHeader bool
	var srv = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				sawAuthHeader = true
			}
			_, _ = w.Write([]byte("<html><title>Instant On 1930</title>" +
				"<form id=login></form></html>"))
		}))
	defer srv.Close()

	var att = proberTo(srv).tryWeb(context.Background(), "10.0.0.12", []int{80, 443},
		[]Credential{{ID: "c1", Name: "admin", Kind: "http",
			Username: "admin", Secret: "s3cret"}},
		Matcher{Driver: "aruba-instant-on-1930"})

	if att.outcome != WebAuthNone {
		t.Fatalf("issue = %q (detail : %s), expected %q", att.outcome, att.detail, WebAuthNone)
	}
	// Le point le plus important du test : le mot de passe n'a been presented a
	// PERSONNE. Le confondre avec un refus enverrait chercher un mauvais mot de
	// passe pendant des heures.
	if sawAuthHeader {
		t.Error("aucun identifiant ne doit partir vers une interface qui n'en demande pas")
	}
	if !strings.Contains(att.detail, "form") {
		t.Errorf("the detail must say where the limit is (connector / form): %q",
			att.detail)
	}
	if att.cred.ID != "" {
		t.Errorf("aucun identifiant ne doit be credite : %q", att.cred.ID)
	}
}

// --- Issue 4 : pas d'identifiant utilisable sur le web, et on le DIT ---

func TestTryWebNoUsableCredential(t *testing.T) {
	var srv = httptest.NewServer(http.HandlerFunc(
		func(_ http.ResponseWriter, _ *http.Request) {
			t.Error("aucune request ne doit partir sans identifiant essayable")
		}))
	defer srv.Close()

	var att = proberTo(srv).tryWeb(context.Background(), "10.0.0.36", []int{80},
		[]Credential{
			{ID: "c1", Name: "ssh", Kind: "ssh", Username: "admin", Secret: "x"},
			{ID: "c2", Name: "telnet", Kind: "telnet", Username: "admin", Secret: "x"},
		},
		Matcher{Driver: "mikrotik-swos"})

	if att.outcome != WebAuthNotTried {
		t.Fatalf("issue = %q, expected %q", att.outcome, WebAuthNotTried)
	}
	// L'absence se DECLARES  : c'est exactement le default qu'on corrige, un
	// identifiant enregistre dont on ne sait pas qu'il n'est jamais essaye.
	if !strings.Contains(att.detail, "http") {
		t.Errorf("le detail doit dire quel genre manque : %q", att.detail)
	}
}

// --- De bout en bout : ce que l'authentification web change au result ---

// Un device qui n'ouvre QUE le 80, dont la page d'accueil ne dit rien de lui
// (ni en-tete `Server`, ni titre) et qui exige une authentification HTTP from la
// root. C'est le cas where le chemin web apporte tout : sans lui, the device
// n'est qu'une adresse avec un port ouvert.
func TestProbeWebAuthLiftsIdentification(t *testing.T) {
	var srv = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var want = "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:s3cret"))
			if r.Header.Get("Authorization") != want {
				w.Header().Set("WWW-Authenticate", `Basic realm="switch"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte("HP ProCurve Switch 2530-24G\nSerial Number : ZA123456\n"))
		}))
	defer srv.Close()

	var prober = &Prober{Dialer: webDialer{
		addr: strings.TrimPrefix(srv.URL, "http://"),
		open: map[int]bool{80: true},
	}}
	var res = prober.Probe(context.Background(), "10.0.0.9",
		[]Credential{{ID: "c1", Name: "admin du parc", Kind: "any",
			Username: "admin", Secret: "s3cret"}})

	if !res.LoginOK || res.WebAuth != WebAuthOK {
		t.Fatalf("login_ok=%v web_auth=%q (%s)", res.LoginOK, res.WebAuth, res.WebAuthDetail)
	}
	// « quel identifiant a fonctionne » est la question que se pose l'exploitant
	// devant un result de sonde : elle doit avoir une response.
	if res.CredentialName != "admin du parc" || res.CredentialID != "c1" {
		t.Errorf("identifiant retenu = %q/%q", res.CredentialID, res.CredentialName)
	}
	if res.Driver != "hpe-procurve" {
		t.Errorf("driver = %q, la page authentifiee devait servir a identifier", res.Driver)
	}
	if res.Serial != "ZA123456" {
		t.Errorf("serial = %q", res.Serial)
	}
	// La confiance monte parce qu'il y a du FAIT NOUVEAU, et pas seulement parce
	// qu'un mot de passe a been accepted.
	if res.Confidence != ConfDeviceSelfReport {
		t.Errorf("confiance = %d, expected %d (page authentifiee de the device)",
			res.Confidence, ConfDeviceSelfReport)
	}
	if !strings.Contains(res.Evidence, "authenticated") {
		t.Errorf("the retained clue must say where it comes from: %q", res.Evidence)
	}
}

// Le genre « telnet » n'est pas essaye : il faut que ca se VOIE sur the device
// where la question se pose, pas seulement dans une page de documentation.
func TestProbeDeclaresTelnetNotTried(t *testing.T) {
	var srv = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<html><title>switch</title></html>"))
		}))
	defer srv.Close()

	var prober = &Prober{Dialer: webDialer{
		addr: strings.TrimPrefix(srv.URL, "http://"),
		// 23 ouvert : c'est there que l'identifiant telnet aurait pu servir.
		open: map[int]bool{23: true, 80: true},
	}}
	var res = prober.Probe(context.Background(), "10.0.0.10",
		[]Credential{{ID: "c1", Name: "telnet d'usine", Kind: "telnet",
			Username: "admin", Secret: "x"}})

	if !strings.Contains(res.Notes, "telnet") || !strings.Contains(res.Notes, "NOT attempted") {
		t.Errorf("the notes must declare that telnet was not attempted: %q", res.Notes)
	}
}

// --- Eligibilite d'un identifiant par transport ---

func TestCredFor(t *testing.T) {
	type row struct {
		kind      string
		ssh, web  bool
		telnetTry bool
	}
	var cases = []row{
		{kind: "ssh", ssh: true},
		{kind: "http", web: true},
		{kind: "telnet", telnetTry: true},
		// « any » et le genre vide valent pour tous les transports ESSAYES.
		{kind: "any", ssh: true, web: true, telnetTry: true},
		{kind: "", ssh: true, web: true, telnetTry: true},
	}
	var c row
	for _, c = range cases {
		var cred = Credential{Kind: c.kind}
		if credFor(cred, "ssh") != c.ssh {
			t.Errorf("genre %q sur ssh = %v, expected %v", c.kind, !c.ssh, c.ssh)
		}
		if credFor(cred, "http") != c.web {
			t.Errorf("genre %q sur http = %v, expected %v", c.kind, !c.web, c.web)
		}
		if credFor(cred, "telnet") != c.telnetTry {
			t.Errorf("genre %q sur telnet = %v, expected %v", c.kind, !c.telnetTry, c.telnetTry)
		}
	}
	if countKind([]Credential{{Kind: "telnet"}, {Kind: "any"}, {Kind: "telnet"}}, "telnet") != 2 {
		t.Error("countKind doit compter les genres DECLARES, sans l'elargissement de credFor")
	}
}

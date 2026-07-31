package driver

import (
	"context"
	"crypto/md5" //nolint:gosec // impose par le protocole Digest, cf. md5hex
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	neturl "net/url"
	"strings"
	"time"
)

// Transport web des connecteurs : un client HTTP dont les connexions partent du
// same endroit que tout le reste — directement, ou par le tunnel d'un proxy.
//
// Le connecteur ne sait pas hence part la connexion : il receives un Dialer. C'est
// ce qui permet au same code de piloter un device joignable localement et un
// device derriere un [[Proxy]] (docs/01-Architecture/Vue d'ensemble).

// Dialer ouvre une connexion TCP. `nil` = connexions directes.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// WebSession est une session HTTP vers l'interface web d'un device : cookies
// conserves, certificat auto-signe accepted, redirections sous control.
type WebSession struct {
	BaseURL string
	// MaxBody borne ce qu'on rapatrie. Nul = MaxBody. Les interfaces recentes
	// servent des bundles JavaScript de plusieurs megaoctets : les tronquer perd
	// justement la partie utile, qui est rarement au start.
	MaxBody int
	client  *http.Client
	auth    Auth
}

// AuthMode : la way dont the device demande a be authentifie AU NIVEAU
// HTTP — i.e. par un en-tete `Authorization`, et non par un formulaire de
// connexion qui pose un cookie.
//
// La distinction n'est pas de forme : une UI a formulaire (l'Instant On) se
// connecte UNE fois et la session vit dans le cookie ; une UI a en-tete (SwOS,
// l'API REST de RouterOS) n'a pas de session du tout et doit re-present  ses
// identifiants a CHAQUE request. Un connecteur qui confond les deux ouvre une
// session par lecture, ou croit be connecte alors qu'il ne l'est plus.
type AuthMode string

const (
	// AuthNone : rien a present .
	AuthNone AuthMode = ""
	// AuthBasic : `Authorization: Basic`. C'est ce que sert l'API REST de
	// RouterOS.
	AuthBasic AuthMode = "basic"
	// AuthDigest : `Authorization: Digest` (RFC 7616, MD5/qop=auth). C'est ce que
	// sert SwOS, et la raison pour laquelle cette mecanique existe ici : sans
	// elle, ses pages de data repondent 401 et the device passe pour muet.
	AuthDigest AuthMode = "digest"
	// AuthAuto : laisser the device dire lequel des deux il veut, dans son
	// `WWW-Authenticate`. A preferer quand on explore une interface inconnue —
	// supposer le schema, c'est confondre « mauvais mot de passe » et « mauvaise
	// method ».
	AuthAuto AuthMode = "auto"
)

// Auth porte les identifiants presented au niveau HTTP.
type Auth struct {
	Mode     AuthMode
	Username string
	Password string
}

// SetAuth arme l'authentification HTTP de la session. Les identifiants ne sont
// used qu'en response a un 401 : une interface qui n'en demande pas ne les
// voit jamais passer.
func (s *WebSession) SetAuth(mode AuthMode, username, password string) {
	s.auth = Auth{Mode: mode, Username: username, Password: password}
}

// NewWebSession construit la session. `insecure` est vrai en pratique pour tout
// device network : ils present  un certificat auto-signe, c'est la norme et
// non une anomalie — la confidentialite du lien est assuree par le tunnel.
func NewWebSession(base string, d Dialer, timeout time.Duration) *WebSession {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	var jar, _ = cookiejar.New(nil)
	var transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		// Un device qui n'accepte que 5 sessions au total ne doit pas voir
		// le connecteur en ouvrir des dizaines : une seule connexion reused.
		MaxIdleConns:        2,
		MaxConnsPerHost:     2,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if d != nil {
		transport.DialContext = d.DialContext
	}
	return &WebSession{
		BaseURL: strings.TrimSuffix(base, "/"),
		client: &http.Client{
			Timeout:   timeout,
			Jar:       jar,
			Transport: transport,
			// Les redirections sont suivies, mais bornees : une UI d'device
			// qui boucle ne doit pas boucler chez nous.
			CheckRedirect: func(r *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
	}
}

// Response est ce qu'un connecteur voit d'une request.
type Response struct {
	Status  int                 `json:"status"`
	URL     string              `json:"url"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
	// Truncated dit que le corps a been coupe : une response tronquee ne doit
	// jamais passer pour complete.
	Truncated bool `json:"truncated"`
	// Binary : le corps n'est pas du texte exploitable (image, archive).
	Binary bool `json:"binary"`
}

// MaxBody borne ce qu'on rapatrie d'une page d'device.
//
// 2 Mio et non 512 Kio : le bundle d'un Instant On 1930 depasse le demi-megaoctet,
// et la troncature coupait precisely la fin — celle qui porte les fonctions
// utiles. Une borne qui ampute en silence la partie qu'on cherche ne protege de
// rien : elle fait conclure a une absence.
const MaxBody = 2 << 20

// Get lit une URL (absolue, ou relative a BaseURL). Lecture seule : rien n'est
// modifie sur the device, ce qui permet d'explorer une interface inconnue sans
// risque.
func (s *WebSession) Get(ctx context.Context, path string) (*Response, error) {
	return s.do(ctx, http.MethodGet, path, "", nil)
}

// PostForm envoie un formulaire. Reserve aux connecteurs : c'est une write.
func (s *WebSession) PostForm(ctx context.Context, path, body string) (*Response, error) {
	return s.do(ctx, http.MethodPost, path, body,
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}

// PostText envoie un corps en `text/plain`. Reserve aux connecteurs : c'est une
// write.
//
// Le type de contenu n'est pas un detail cosmetique sur les interfaces
// embarquees : c'est SwOS qui a impose cette variante, son interface annoncant
// `text/plain` pour toutes ses ecritures. Les octets du corps sont exactement
// ceux de `PostForm` — aucun de ces deux appels ne reencode quoi que ce soit —
// mais un firmware qui n'a jamais vu qu'un seul type de contenu est en droit de
// refuser l'autre, et les refus de cette gamme sont MUETS : un 200 sans effet.
// Mieux vaut donc envoyer ce que the device attend que decouvrir sur le terrain
// qu'une pose s'est declared reussie sans rien changer.
func (s *WebSession) PostText(ctx context.Context, path, body string) (*Response, error) {
	return s.do(ctx, http.MethodPost, path, body,
		map[string]string{"Content-Type": "text/plain"})
}

// PostAJAX envoie un corps tel quel avec les en-tetes qu'un `$.ajax` de jQuery
// produit : type de contenu formulaire, et `X-Requested-With`. Beaucoup d'UI
// embarquees n'acceptent leurs propres ecritures que sous cette forme — c'est
// la seule qu'elles ont jamais vue.
func (s *WebSession) PostAJAX(ctx context.Context, path, body string) (*Response, error) {
	return s.do(ctx, http.MethodPost, path, body, map[string]string{
		"Content-Type":     "application/x-www-form-urlencoded; charset=UTF-8",
		"X-Requested-With": "XMLHttpRequest",
		"Accept":           "application/xml, text/xml, */*; q=0.01",
	})
}

// Put envoie un corps JSON en PUT. Reserve aux connecteurs : c'est une write.
func (s *WebSession) Put(ctx context.Context, path, body string) (*Response, error) {
	return s.do(ctx, http.MethodPut, path, body,
		map[string]string{"Content-Type": "application/json"})
}

// PostJSON envoie un corps JSON. Reserve aux connecteurs : c'est une write.
func (s *WebSession) PostJSON(ctx context.Context, path, body string) (*Response, error) {
	return s.do(ctx, http.MethodPost, path, body,
		map[string]string{"Content-Type": "application/json"})
}

func (s *WebSession) do(ctx context.Context, method, path, body string,
	headers map[string]string) (*Response, error) {
	var url = path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		url = s.BaseURL + path
	}
	var resp, doErr = s.send(ctx, method, url, body, headers, "")
	if doErr != nil {
		return nil, doErr
	}
	// 401 : the device dit COMMENT il veut be authentifie. On ne le devine
	// pas, on le lit — puis on rejoue la same request une fois, et une seule. Un
	// mot de passe refused doit rester un refus, pas une boucle de tentatives qui
	// finirait par verrouiller le compte.
	if resp.StatusCode == http.StatusUnauthorized && s.auth.Mode != AuthNone {
		var challenge = resp.Header.Get("WWW-Authenticate")
		resp.Body.Close() //nolint:errcheck
		var credential, credErr = s.authorization(method, url, challenge)
		if credErr != nil {
			return nil, credErr
		}
		resp, doErr = s.send(ctx, method, url, body, headers, credential)
		if doErr != nil {
			return nil, doErr
		}
	}
	defer resp.Body.Close() //nolint:errcheck

	var err error

	var limit = s.MaxBody
	if limit <= 0 {
		limit = MaxBody
	}
	var raw []byte
	raw, err = io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	var out = &Response{
		Status: resp.StatusCode, URL: resp.Request.URL.String(),
		Headers: resp.Header,
	}
	if len(raw) > limit {
		raw = raw[:limit]
		out.Truncated = true
	}
	out.Binary = !looksTextual(raw)
	if !out.Binary {
		out.Body = string(raw)
	}
	return out, nil
}

// send emet une request et rend la response brute, corps NON lu : c'est do qui
// decide de le lire ou de rejouer la request authentifiee.
func (s *WebSession) send(ctx context.Context, method, url, body string,
	headers map[string]string, credential string) (*http.Response, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var req, err = http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	// Un User-Agent honnete : l'administrateur qui lit les journaux de son
	// device doit pouvoir identifier ce qui l'interroge.
	req.Header.Set("User-Agent", "meshcraft")
	req.Header.Set("Accept", "*/*")
	var k, v string
	for k, v = range headers {
		req.Header.Set(k, v)
	}
	if credential != "" {
		req.Header.Set("Authorization", credential)
	}
	return s.client.Do(req)
}

// authorization construit l'en-tete `Authorization` reclame par le challenge.
//
// Le schema vient de the device, pas de nous : `auto` le lit dans le
// challenge, et un mode explicite n'est retenu que s'il correspond a ce qui est
// propose. Presenter du Basic a qui demande du Digest donne un second 401, qu'on
// lirait comme un mot de passe faux — l'erreur la plus couteuse a diagnostiquer
// sur un device dont on n'a pas la console.
func (s *WebSession) authorization(method, url, challenge string) (string, error) {
	var scheme = strings.ToLower(strings.Fields(strings.TrimSpace(challenge) + " ")[0])
	switch {
	case scheme == "digest" && s.auth.Mode != AuthBasic:
		return s.digestCredential(method, url, challenge)
	case scheme == "basic" && s.auth.Mode != AuthDigest:
		return "Basic " + base64.StdEncoding.EncodeToString(
			[]byte(s.auth.Username+":"+s.auth.Password)), nil
	case challenge == "" && s.auth.Mode == AuthBasic:
		// Pas de challenge lisible mais du Basic requested explicitement : c'est le
		// seul schema qu'on puisse present  sans rien savoir de the device.
		return "Basic " + base64.StdEncoding.EncodeToString(
			[]byte(s.auth.Username+":"+s.auth.Password)), nil
	}
	return "", fmt.Errorf("authentification HTTP %q demandee par the device, "+
		"non managed par ce connecteur (challenge : %q)", scheme, challenge)
}

// digestCredential calcule la response Digest MD5 avec qop=auth (RFC 7616 §3.4),
// la seule variante que servent les interfaces embarquees rencontrees.
//
// `cnonce` est tire au hasard a chaque appel et `nc` reste a 1 : on ne reutilise
// jamais un nonce d'une request a l'autre. C'est ce qui rend chaque request
// independante — un connecteur qui incrementerait un compteur partage serait
// faux from qu'il lit deux pages en parallele.
func (s *WebSession) digestCredential(method, url, challenge string) (string, error) {
	var p = parseChallenge(challenge)
	var realm, nonce = p["realm"], p["nonce"]
	if nonce == "" {
		return "", fmt.Errorf("challenge Digest sans nonce : %q", challenge)
	}
	var algorithm = strings.ToUpper(p["algorithm"])
	if algorithm != "" && algorithm != "MD5" {
		return "", fmt.Errorf("Digest %s non managed (seul MD5 l'est)", algorithm)
	}
	// L'URI du digest est le chemin, pas l'URL absolue : c'est ce que
	// the device a vu passer sur sa ligne de request.
	var uri = url
	var parsed, parseErr = neturl.Parse(url)
	if parseErr == nil {
		uri = parsed.RequestURI()
	}

	var ha1 = md5hex(s.auth.Username + ":" + realm + ":" + s.auth.Password)
	var ha2 = md5hex(method + ":" + uri)

	var raw = make([]byte, 8)
	var _, randErr = rand.Read(raw)
	if randErr != nil {
		return "", randErr
	}
	var cnonce = hex.EncodeToString(raw)

	var qop = ""
	var field string
	for _, field = range strings.Split(p["qop"], ",") {
		if strings.TrimSpace(field) == "auth" {
			qop = "auth"
		}
	}

	var response string
	var parts = []string{
		`username="` + s.auth.Username + `"`,
		`realm="` + realm + `"`,
		`nonce="` + nonce + `"`,
		`uri="` + uri + `"`,
	}
	if qop == "auth" {
		response = md5hex(ha1 + ":" + nonce + ":00000001:" + cnonce + ":auth:" + ha2)
		parts = append(parts, "qop=auth", "nc=00000001", `cnonce="`+cnonce+`"`)
	} else {
		// Digest « historique », sans qop : kept parce que des firmwares anciens
		// n'annoncent rien d'autre, et qu'un refus ici renverrait l'exploitant a
		// une interface qui, elle, fonctionne dans son navigateur.
		response = md5hex(ha1 + ":" + nonce + ":" + ha2)
	}
	parts = append(parts, `response="`+response+`"`)
	if p["opaque"] != "" {
		parts = append(parts, `opaque="`+p["opaque"]+`"`)
	}
	return "Digest " + strings.Join(parts, ", "), nil
}

// parseChallenge decoupe un `WWW-Authenticate` en paires key/valeur, guillemets
// retires. Tolerant par necessity : les firmwares embarques ponctuent leur
// challenge chacun a leur way.
func parseChallenge(challenge string) map[string]string {
	var out = map[string]string{}
	var body = strings.TrimSpace(challenge)
	var space = strings.IndexByte(body, ' ')
	if space < 0 {
		return out
	}
	body = body[space+1:]
	var field string
	for _, field = range strings.Split(body, ",") {
		var eq = strings.IndexByte(field, '=')
		if eq < 0 {
			continue
		}
		var key = strings.ToLower(strings.TrimSpace(field[:eq]))
		var value = strings.Trim(strings.TrimSpace(field[eq+1:]), `"`)
		out[key] = value
	}
	return out
}

// md5hex : MD5 impose par le protocole Digest, et employed nulle part ailleurs
// dans meshcraft. Ce n'est pas un choix de security — c'est le seul digest que
// ces interfaces acceptent, et le lien lui-same est protege par le tunnel.
func md5hex(s string) string {
	var sum = md5.Sum([]byte(s)) //nolint:gosec
	return hex.EncodeToString(sum[:])
}

// looksTextual : un octet nul dans les premiers kilo-octets suffit a conclure.
func looksTextual(b []byte) bool {
	var limit = len(b)
	if limit > 2048 {
		limit = 2048
	}
	var i int
	for i = 0; i < limit; i++ {
		if b[i] == 0 {
			return false
		}
	}
	return true
}

package discovery

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"meshcraft/internal/driver"
	"meshcraft/internal/driver/instanton"
	// Les connecteurs s'enregistrent aussi aupres du registre driver par leur init :
	// la discovery est le point du programme qui les consulte tous, c'est donc ici
	// qu'ils entrent en scene.
	"meshcraft/internal/driver/mikrotik/routeros"
	"meshcraft/internal/driver/mikrotik/swos"
)

// Ports sondes : ceux qui distinguent un device network administrable.
var ProbePorts = []int{22, 23, 80, 443, 8080, 8443}

const (
	dialTimeout  = 900 * time.Millisecond
	bannerWait   = 1500 * time.Millisecond
	httpTimeout  = 3 * time.Second
	webAuthWait  = 5 * time.Second
	sshTimeout   = 6 * time.Second
	shellBudget  = 12 * time.Second
	maxOutputLen = 256 * 1024
)

// Credential est un identifiant a essayer. Secret = mot de passe en clair,
// dechiffre juste avant l'essai et jamais journalise.
type Credential struct {
	ID   string
	Name string
	// Kind : le transport sur lequel cet identifiant a un sens.
	//
	// « ssh » et « http » sont essayes, « telnet » ne l'est pas (cf. la note du
	// same nom plus bas). « any » vaut pour tous les transports essayes, et le
	// VIDE en fait autant : c'est le cas de l'identifiant ponctuel saisi pour un
	// seul essai, where l'exploitant ne devrait pas avoir a deviner par where meshcraft
	// va entrer — il donne un couple utilisateur/mot de passe, on l'essaie par
	// tous les chemins qu'on sait emprunter.
	Kind     string // ssh | telnet | http | any | "" (tous)
	Username string
	Secret   string
}

// credFor dit si un identifiant est a essayer sur ce transport.
func credFor(c Credential, transport string) bool {
	switch c.Kind {
	case "", "any":
		return true
	default:
		return c.Kind == transport
	}
}

// countKind compte les identifiants d'un genre donne, tel qu'il a been declared :
// ici on ne veut PAS l'elargissement de credFor, mais bien « combien
// d'identifiants ont been enregistres pour ce transport-there ».
func countKind(creds []Credential, kind string) int {
	var n int
	var c Credential
	for _, c = range creds {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

// Result rassemble tout ce qu'on a appris d'une adresse.
type Result struct {
	IP         string   `json:"ip"`
	Reachable  bool     `json:"reachable"`
	LatencyMS  int      `json:"latency_ms"`
	OpenPorts  []int    `json:"open_ports"`
	Transports []string `json:"transports"`

	SSHBanner  string `json:"ssh_banner,omitempty"`
	HTTPServer string `json:"http_server,omitempty"`
	HTTPTitle  string `json:"http_title,omitempty"`

	// Renseignes seulement si un identifiant a fonctionne.
	LoginOK        bool   `json:"login_ok"`
	CredentialID   string `json:"credential_id,omitempty"`
	CredentialName string `json:"credential_name,omitempty"`
	ShellOutput    string `json:"shell_output,omitempty"`
	RawConfig      string `json:"raw_config,omitempty"`

	// Issue de l'essai des identifiants sur l'interface web, et sa phrase.
	//
	// Ce champ existe parce que « pas authentifie » recouvre trois situations qui
	// n'appellent pas du tout le same geste de l'exploitant, cf. les constantes
	// WebAuth*. Sans lui, l'UI ne pourrait afficher qu'un failure, et le seul geste
	// qu'un failure suggere est de rechercher un mot de passe — y compris there where
	// aucun mot de passe n'a jamais been presented a personne.
	WebAuth       string `json:"web_auth,omitempty"`
	WebAuthDetail string `json:"web_auth_detail,omitempty"`

	// MAC et PortGroups viennent du connecteur quand il sait les lire.
	MAC        string           `json:"mac,omitempty"`
	PortGroups []map[string]any `json:"port_groups,omitempty"`

	Driver     string `json:"driver"`
	Vendor     string `json:"vendor"`
	Model      string `json:"model"`
	Hostname   string `json:"hostname"`
	Serial     string `json:"serial"`
	Confidence int    `json:"confidence"`
	Evidence   string `json:"evidence"`
	Notes      string `json:"notes,omitempty"`
}

// Dialer ouvre les connexions TCP de la sonde. C'est le point ingress du
// tunnel : `net.Dialer` pour le proxy « Local » (l'instance elle-same), le hub
// de tunnels pour un proxy distant. La sonde, elle, ne sait pas hence elle part —
// c'est la rule « un seul chemin de code » (docs/01-Architecture/Vue d'ensemble).
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Prober sonde une adresse. Sans etat : sur en concurrence.
type Prober struct {
	// Dialer vide = connexions directes depuis le controleur.
	Dialer Dialer

	// FetchConfig : lire aussi la configuration courante (plus long, et ca
	// laisse une trace dans les logs de the device). Utilise pour l'ajout
	// manuel et l'import de configuration, pas pour le balayage de masse.
	FetchConfig bool
	// ForceDriver contraint le driver (type choisi par l'utilisateur).
	ForceDriver string
}

// Probe execute la sequence complete sur une adresse.
func (p *Prober) Probe(ctx context.Context, ip string, creds []Credential) Result {
	var res = Result{IP: ip, OpenPorts: []int{}, Transports: []string{}}

	var started = time.Now()
	// Les ports sont sondes EN PARALLELE : en serial, un hote mort coutait
	// 6 × 900 ms, ce qui rendait un /16 hors de portee (des heures). En parallele
	// il costs 900 ms, et le balayage d'un /16 redevient une affaire de minutes.
	var openPorts = p.scanPorts(ctx, ip)
	res.OpenPorts = openPorts
	if len(res.OpenPorts) == 0 {
		return res
	}
	res.Reachable = true
	res.LatencyMS = int(time.Since(started).Milliseconds())
	res.Transports = transportsOf(res.OpenPorts)

	if hasPort(res.OpenPorts, 22) {
		res.SSHBanner = p.grabBanner(ctx, ip, 22)
	} else if hasPort(res.OpenPorts, 23) {
		res.SSHBanner = ""
		res.Notes = strings.TrimSpace(res.Notes + " Telnet ouvert : identification par banniere seulement.")
	}
	if hasPort(res.OpenPorts, 443) || hasPort(res.OpenPorts, 8443) {
		res.HTTPServer, res.HTTPTitle = p.grabHTTP(ctx, ip, httpsPort(res.OpenPorts), true)
	}
	if res.HTTPServer == "" && res.HTTPTitle == "" &&
		(hasPort(res.OpenPorts, 80) || hasPort(res.OpenPorts, 8080)) {
		res.HTTPServer, res.HTTPTitle = p.grabHTTP(ctx, ip, httpPort(res.OpenPorts), false)
	}

	// Premiere identification, sans authentification.
	var m, conf, evidence = Identify("", res.SSHBanner, res.HTTPServer, res.HTTPTitle)
	if p.ForceDriver != "" {
		m = MatcherFor(p.ForceDriver)
		evidence = "type impose par l'utilisateur"
		conf = ConfShellOutput
	}

	// winner est l'identifiant qui a ouvert la porte, kept en clair le temps de la
	// sonde pour be RENDU au connecteur qui va lire plus profond
	// (cf. enrich). Il ne quitte jamais cette fonction autrement : ni dans le
	// Result, ni dans une note, ni dans un journal.
	var winner Credential
	var haveWinner bool

	// Essai des identifiants sur SSH, puis identification renforcee.
	if hasPort(res.OpenPorts, 22) && len(creds) > 0 {
		var out, cred, err = p.trySSH(ctx, ip, creds, m)
		if cred.Username != "" {
			// Retenu from que la session s'est ouverte, same si le shell n'a rien
			// repondu : c'est le cas de RouterOS, dont l'API REST se laissera lire
			// avec cet identifiant-there alors que son shell reste muet.
			winner, haveWinner = cred, true
		}
		if err != nil {
			res.Notes = strings.TrimSpace(res.Notes + " " + err.Error())
		}
		if out != "" {
			res.LoginOK = true
			res.CredentialID = cred.ID
			res.CredentialName = cred.Name
			res.ShellOutput = out
			if p.ForceDriver == "" {
				m, conf, evidence = Identify(out, res.SSHBanner, res.HTTPServer, res.HTTPTitle)
			}
			res.Hostname, res.Serial, res.Model = ExtractFacts(out)
			if p.FetchConfig {
				res.RawConfig = extractConfig(out)
			}
		}
	}

	// Essai des identifiants sur l'interface web, puis identification renforcee.
	//
	// Apres SSH, et seulement si SSH n'a rien ouvert : un device qui a already
	// parle par sa CLI n'a rien a gagner d'un second essai, et chaque essai laisse
	// une ligne dans le journal d'authentification de the device. Le chemin web
	// n'est pas un luxe pour autant — il est le SEUL sur les gammes web-only, et
	// le seul qui reste quand la sonde SSH tombe en delai depasse alors que l'API
	// REST du same device repond.
	if !res.LoginOK && len(creds) > 0 && webPortOpen(res.OpenPorts) {
		var att = p.tryWeb(ctx, ip, res.OpenPorts, creds, m)
		res.WebAuth, res.WebAuthDetail = att.outcome, att.detail
		if att.outcome != WebAuthOK && att.detail != "" {
			// Une issue non concluante doit SURVIVRE au result de sonde : la note
			// part dans le candidat, et c'est there qu'on la relira dans trois jours.
			res.Notes = strings.TrimSpace(res.Notes + " " + att.detail)
		}
		if att.outcome == WebAuthOK {
			res.LoginOK = true
			res.CredentialID = att.cred.ID
			res.CredentialName = att.cred.Name
			winner, haveWinner = att.cred, true

			// Ce que la page authentifiee apprend, et qui n'was pas accessible a
			// un anonyme. On ne remplace jamais un fait already connu : celui-ci vient
			// d'une lecture generique, celui-there peut venir d'un connecteur.
			var host, serial, mdl = ExtractFacts(att.body)
			var facts bool
			if res.Hostname == "" && host != "" {
				res.Hostname, facts = host, true
			}
			if res.Serial == "" && serial != "" {
				res.Serial, facts = serial, true
			}
			if res.Model == "" && mdl != "" {
				res.Model, facts = mdl, true
			}
			// La page authentifiee ne se voit confier l'identification que si rien
			// d'autre n'y est parvenu. C'est du texte riche — une UI d'device
			// peut nommer un autre constructeur au passage, dans une note de
			// compatibilite — et laisser un corps de page defaire une reconnaissance
			// obtenue par la banniere SSH serait une regression, pas un progres.
			if p.ForceDriver == "" && isGenericDriver(m.Driver) {
				var wm = identifyFromWeb(att.body)
				if wm.Driver != "" {
					m, facts = wm, true
				}
			}
			// La confiance monte, mais seulement si l'authentification a rapporte
			// quelque chose : reussir a se connecter ne prouve pas qu'on a mieux
			// identifie the device, et une confiance qui monte sans fait nouveau
			// est un mensonge tranquille. Quand il y a bien du fait nouveau, il vaut
			// ConfDeviceSelfReport : c'est the device qui parle de lui-same,
			// derriere une authentification qu'il a lui-same exigee — pas une regex
			// sur un titre HTML.
			if facts && conf < ConfDeviceSelfReport {
				conf = ConfDeviceSelfReport
				evidence = "page authentifiee de the device (" + att.path + ")"
			}
			// Une configuration demandee mais non lue doit se DIRE. L'exploitant a
			// coche « lire aussi la configuration » et l'authentification a reussi :
			// sans cette phrase, il conclurait a un device qui refuse de livrer
			// sa configuration, alors que c'est meshcraft qui ne sait pas encore la
			// lire par ce chemin — l'export d'une gamme web-only appartient a son
			// connecteur, pas a une sonde generique.
			if p.FetchConfig && res.RawConfig == "" {
				res.Notes = strings.TrimSpace(res.Notes +
					" Configuration non lue : access s'est fait par l'interface web," +
					" dont l'export appartient au connecteur de la gamme.")
			}
		}
	}

	// Le genre « telnet » ne se laisse PAS au silence : un identifiant enregistre
	// qu'on n'essaie jamais est exactement le default qu'on vient de corriger pour
	// le web. On le dit ici, sur the device where la question se pose vraiment —
	// celui dont le 23 est ouvert.
	if hasPort(res.OpenPorts, 23) {
		var n = countKind(creds, "telnet")
		if n > 0 {
			res.Notes = strings.TrimSpace(res.Notes + fmt.Sprintf(
				" %d identifiant(s) de genre telnet enregistres : NON essayes,"+
					" meshcraft n'a pas de client telnet.", n))
		}
	}

	res.Driver = m.Driver
	res.Vendor = m.Vendor
	res.Confidence = conf
	res.Evidence = evidence

	// Le connecteur reconnu a le droit de preciser ce que la sonde generique ne
	// sait pas voir : model exact, serial number, front panel de ports. C'est du
	// savoir de plateforme, il n'a rien a faire dans la sonde.
	p.enrich(ctx, &res, winner, haveWinner)
	if res.Hostname == "" && res.HTTPTitle != "" {
		res.Hostname = ""
	}
	return res
}

// scanPorts sonde tous les ports en parallele et renvoie ceux qui repondent,
// dans l'ordre de ProbePorts (un result stable est plus facile a comparer).
func (p *Prober) scanPorts(ctx context.Context, ip string) []int {
	var open = make([]bool, len(ProbePorts))
	var wg sync.WaitGroup
	var i int
	var port int
	for i, port = range ProbePorts {
		wg.Add(1)
		go func(idx, prt int) {
			defer wg.Done()
			open[idx] = p.dialOK(ctx, ip, prt)
		}(i, port)
	}
	wg.Wait()
	var out = []int{}
	for i, port = range ProbePorts {
		if open[i] {
			out = append(out, port)
		}
	}
	return out
}

// dial ouvre une connexion, par le tunnel du proxy choisi ou en direct.
func (p *Prober) dial(ctx context.Context, addr string, timeout time.Duration) (net.Conn, error) {
	if p.Dialer != nil {
		var dctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
		return p.Dialer.DialContext(dctx, "tcp", addr)
	}
	var d = net.Dialer{Timeout: timeout}
	return d.DialContext(ctx, "tcp", addr)
}

func (p *Prober) dialOK(ctx context.Context, ip string, port int) bool {
	var conn, err = p.dial(ctx, net.JoinHostPort(ip, itoa(port)), dialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// grabBanner lit la banniere que le serveur envoie SPONTANEMENT (cas de SSH et
// de beaucoup de serveurs Telnet). Aucune tentative d'authentification ici :
// c'est gratuit et sans risque de verrouillage de compte.
func (p *Prober) grabBanner(ctx context.Context, ip string, port int) string {
	var conn, err = p.dial(ctx, net.JoinHostPort(ip, itoa(port)), dialTimeout)
	if err != nil {
		return ""
	}
	defer conn.Close() //nolint:errcheck
	_ = conn.SetReadDeadline(time.Now().Add(bannerWait))
	var buf = make([]byte, 512)
	var n int
	n, err = conn.Read(buf)
	if n <= 0 {
		return ""
	}
	return strings.TrimSpace(sanitize(string(buf[:n])))
}

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

func (p *Prober) grabHTTP(ctx context.Context, ip string, port int, secure bool) (server, title string) {
	var scheme = "http"
	if secure {
		scheme = "https"
	}
	// InsecureSkipVerify : les devices network present  des certificats
	// auto-signes, c'est la norme. On ne fait ici que de l'identification.
	var client = &http.Client{
		Timeout: httpTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			// Le HTTP passe par le same chemin que le reste : c'est ce qui
			// permet d'atteindre l'UI web d'un device a travers un proxy.
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return p.dial(ctx, addr, dialTimeout)
			},
		},
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 2 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	var url = fmt.Sprintf("%s://%s/", scheme, net.JoinHostPort(ip, itoa(port)))
	var req, err = http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", ""
	}
	var resp *http.Response
	resp, err = client.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close() //nolint:errcheck
	server = sanitize(resp.Header.Get("Server"))
	if server == "" {
		server = sanitize(resp.Header.Get("WWW-Authenticate"))
	}
	var body []byte
	body, err = io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err == nil {
		var m = titleRe.FindSubmatch(body)
		if len(m) > 1 {
			title = sanitize(strings.TrimSpace(string(m[1])))
		}
	}
	return server, title
}

// sshClientConfig accepte volontairement les algorithmes anciens : sans eux,
// une bonne partie du parc reel (switchs de plus de cinq ans) est injoignable.
// La key d'hote n'est pas verified — en discovery on ne la connait pas encore.
func sshClientConfig(c Credential) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User: c.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(c.Secret),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				var answers = make([]string, len(questions))
				var i int
				for i = range questions {
					answers[i] = c.Secret
				}
				return answers, nil
			}),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         sshTimeout,
		Config: ssh.Config{
			KeyExchanges: []string{
				"curve25519-sha256", "curve25519-sha256@libssh.org",
				"ecdh-sha2-nistp256", "ecdh-sha2-nistp384", "ecdh-sha2-nistp521",
				"diffie-hellman-group14-sha256", "diffie-hellman-group14-sha1",
				"diffie-hellman-group1-sha1", "diffie-hellman-group-exchange-sha1",
			},
			Ciphers: []string{
				"aes128-gcm@openssh.com", "aes256-gcm@openssh.com",
				"aes128-ctr", "aes192-ctr", "aes256-ctr",
				"aes128-cbc", "aes256-cbc", "3des-cbc",
			},
		},
		HostKeyAlgorithms: []string{
			"ssh-ed25519", "rsa-sha2-256", "rsa-sha2-512", "ssh-rsa",
			"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384",
		},
	}
}

// maxAuthFailures borne les essais par hote : au-dela, on stops same s'il
// reste des identifiants a tester. Les politiques AAA verrouillent les comptes
// after quelques echecs — decouvrir un switch ne doit pas bloquer access de
// l'equipe network (cf. docs/01-Architecture/Discovery, garde-fous).
const maxAuthFailures = 2

// trySSH essaie les identifiants dans l'ordre recu et, au premier qui passe,
// execute les commandes d'identification du driver.
// trySSH rend l'identifiant qui a fonctionne, et non son seul identifiant de
// base : un identifiant saisi pour un seul essai n'a PAS d'ID (rien ne
// l'enregistre), et le retrouver after coup dans la liste par son ID was
// impossible. Or c'est precisely lui qu'un operator emploie quand il ajoute un
// device a la main — le cas where l'enrichissement compte le plus.
func (p *Prober) trySSH(ctx context.Context, ip string, creds []Credential, m Matcher) (out string, cred Credential, err error) {
	var failures int
	var c Credential
	for _, c = range creds {
		if ctx.Err() != nil {
			return "", Credential{}, ctx.Err()
		}
		if !credFor(c, "ssh") {
			continue
		}
		if failures >= maxAuthFailures {
			return "", Credential{}, fmt.Errorf(
				"essais d'identifiants arretes after %d echecs (protection contre le verrouillage de compte)",
				failures)
		}
		var client, dialErr = p.sshDial(ctx, ip, c)
		if dialErr != nil {
			if isAuthFailure(dialErr) {
				failures++
				continue
			}
			return "", Credential{}, fmt.Errorf("SSH : %v", firstLine(dialErr.Error()))
		}
		var text, shellErr = runShell(ctx, client, p.commandsFor(m))
		_ = client.Close()
		if shellErr != nil && text == "" {
			// L'identifiant est rendu DESPITE l'erreur : la session s'est ouverte, donc
			// le mot de passe est bon — c'est le shell qui n'a pas repondu comme on
			// l'attendait. C'est exactement le cas de RouterOS, dont le shell
			// interactif ne se laisse pas gratter, et il ne faut pas que ce refus de
			// dialogue fasse perdre un identifiant qui, lui, fonctionne.
			return "", c, fmt.Errorf("session SSH : %v", firstLine(shellErr.Error()))
		}
		return text, c, nil
	}
	if failures > 0 {
		return "", Credential{}, fmt.Errorf("aucun identifiant standard accepted (%d refus)", failures)
	}
	return "", Credential{}, nil
}

func (p *Prober) sshDial(ctx context.Context, ip string, c Credential) (*ssh.Client, error) {
	var conn, err = p.dial(ctx, net.JoinHostPort(ip, "22"), sshTimeout)
	if err != nil {
		return nil, err
	}
	var sc ssh.Conn
	var chans <-chan ssh.NewChannel
	var reqs <-chan *ssh.Request
	sc, chans, reqs, err = ssh.NewClientConn(conn, net.JoinHostPort(ip, "22"), sshClientConfig(c))
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return ssh.NewClient(sc, chans, reqs), nil
}

func (p *Prober) commandsFor(m Matcher) []string {
	var cmds = []string{}
	if m.PageCmd != "" {
		cmds = append(cmds, m.PageCmd)
	}
	if m.IdentCmd != "" {
		cmds = append(cmds, m.IdentCmd)
	}
	if p.FetchConfig && m.ConfigCmd != "" {
		cmds = append(cmds, configMarkerBegin, m.ConfigCmd, configMarkerEnd)
	}
	return cmds
}

// Marqueurs encadrant la configuration dans la sortie du shell. Ce sont des
// commandes volontairement invalides : the device les rejette, mais leur
// echo dans la sortie delimite proprement le bloc de configuration.
const (
	configMarkerBegin = "echo MESHCRAFT-CONFIG-BEGIN"
	configMarkerEnd   = "echo MESHCRAFT-CONFIG-END"
)

// runShell ouvre un shell interactif (beaucoup de switchs refusent `exec`),
// envoie les commandes et lit la sortie pendant un temps borne.
func runShell(ctx context.Context, client *ssh.Client, cmds []string) (string, error) {
	var session, err = client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close() //nolint:errcheck

	var stdin io.WriteCloser
	stdin, err = session.StdinPipe()
	if err != nil {
		return "", err
	}
	var stdout io.Reader
	stdout, err = session.StdoutPipe()
	if err != nil {
		return "", err
	}

	// Un pty large evite la pagination sur les plateformes qui ignorent la
	// commande de desactivation.
	err = session.RequestPty("vt100", 200, 512, ssh.TerminalModes{
		ssh.ECHO: 0, ssh.TTY_OP_ISPEED: 38400, ssh.TTY_OP_OSPEED: 38400,
	})
	if err != nil {
		return "", err
	}
	if err = session.Shell(); err != nil {
		return "", err
	}

	var collected = make(chan string, 1)
	go func() {
		var b strings.Builder
		var buf = make([]byte, 8192)
		var n int
		var rerr error
		for {
			n, rerr = stdout.Read(buf)
			if n > 0 {
				b.Write(buf[:n])
				if b.Len() > maxOutputLen {
					break
				}
			}
			if rerr != nil {
				break
			}
		}
		collected <- b.String()
	}()

	var cmd string
	for _, cmd = range cmds {
		if _, err = io.WriteString(stdin, cmd+"\n"); err != nil {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	// « exit » referme proprement, ce qui debloque la lecture sans attendre
	// tout le budget.
	_, _ = io.WriteString(stdin, "exit\n")
	_ = stdin.Close()

	var budget = time.NewTimer(shellBudget)
	defer budget.Stop()
	var text string
	select {
	case text = <-collected:
		return sanitize(text), nil
	case <-budget.C:
		return "", fmt.Errorf("delai depasse after %s", shellBudget)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// extractConfig isole le bloc de configuration entre les marqueurs. A default de
// marqueurs, renvoie la sortie entire : le parseur d'import sait ignorer le
// bruit, et une configuration tronquee serait pire qu'une configuration bruitee.
func extractConfig(out string) string {
	var begin = strings.Index(out, "MESHCRAFT-CONFIG-BEGIN")
	var end = strings.LastIndex(out, "MESHCRAFT-CONFIG-END")
	if begin < 0 || end < 0 || end <= begin {
		return out
	}
	var body = out[begin:end]
	var nl = strings.Index(body, "\n")
	if nl < 0 {
		return out
	}
	return strings.TrimSpace(body[nl+1:])
}

// --- Essai des identifiants sur l'interface web ---
//
// Ce que cet essai couvre : l'authentification AU NIVEAU HTTP, celle qui se
// present dans un en-tete `Authorization` parce que the device a repondu 401
// en annoncant lui-same le schema qu'il veut (driver.AuthAuto — on ne le devine
// pas, cf. internal/driver/webfetch.go). Deux gammes du parc de test n'ont rien
// d'autre : un SwOS n'ouvre que le port 80 et sert ses pages de data en
// Digest, et l'API REST de RouterOS repond en Basic sur un device dont la
// sonde SSH tombe en delai depasse. Avant cet essai, la discovery les voyait
// sans jamais pouvoir se present .
//
// Ce que cet essai ne couvre PAS, et ne pourra pas couvrir : les UI a
// FORMULAIRE. Un Aruba Instant On rend 200 a un anonyme et n'ouvre de session
// qu'after le POST d'un formulaire dont il faut connaitre l'URL, les champs, et
// la way dont le cookie est applied. Rien de cela ne se devine depuis une sonde
// generique : c'est du savoir de plateforme, donc le travail d'un connecteur
// (docs/01-Architecture/Connecteurs). C'est exactement la raison d'be de la
// troisieme issue ci-dessous — sans elle, cette limite se lirait comme un mot de
// passe refused.
const (
	// WebAuthNotTried : essai non conduit — aucun port web, aucun identifiant
	// utilisable sur le web, ou SSH avait already ouvert une session.
	WebAuthNotTried = ""
	// WebAuthOK : the device a accepted un identifiant.
	WebAuthOK = "ok"
	// WebAuthRefused : the device a REDEMANDE a s'authentifier after avoir vu
	// nos identifiants. There, et there seulement, il y a un mot de passe a corriger.
	WebAuthRefused = "refused"
	// WebAuthNone : aucune des pages essayees ne demande d'authentification HTTP.
	// Ce n'est PAS un failure d'identifiants — aucun n'a been presented a personne.
	// Confondre les deux, c'est envoyer chercher un mauvais mot de passe pendant
	// des heures sur une interface qui n'en attendait aucun.
	WebAuthNone = "no-http-auth"
	// WebAuthError : l'essai n'a pas abouti (lien coupe, TLS refused, schema
	// d'authentification inconnu, response qu'on ne sait pas interpreter). Ni un
	// refus, ni une absence : une panne d'essai, qui se relance.
	WebAuthError = "error"
)

// webAuthPaths dit WHERE chercher un 401 sur une plateforme already reconnue.
//
// La root ne suffit pas, et c'est verified sur les deux devices de test :
// SwOS et RouterOS servent tous deux leur page d'accueil en 200 a un anonyme, et
// n'exigent l'authentification que sur leurs pages de DONNEES. S'en tenir a « / »
// aurait donc conclu « cette interface n'authentifie pas » sur les deux
// devices que ce travail doit debloquer.
//
// Cette table est le strict minimum, et elle a la same faiblesse que celle des
// motifs de fingerprint.go : elle est du savoir de plateforme range dans une
// sonde generique. Sa place definitive est dans le connecteur, qui sait already where
// vivent ses pages ; en attendant, l'avoir ici est la difference entre un
// identifiant essaye et un identifiant decoratif.
var webAuthPaths = map[string][]string{
	// Pages `*.b` de SwOS : Digest, realm = le model du switch.
	"mikrotik-swos": {"/sys.b"},
	// API REST de RouterOS : Basic. `system/resource` est la ressource la moins
	// engageante qui exige l'authentification.
	"mikrotik-routeros": {"/rest/system/resource"},
}

// webRootOnly : a default de plateforme reconnue, la root et rien d'autre.
// Deviner des chemins sur une interface inconnue, ce serait balayer the device
// a l'aveugle — beaucoup de bruit dans ses journaux pour un pari.
var webRootOnly = []string{"/"}

// webAttempt : ce qu'on rapporte d'un essai web. `body` reste local a la sonde ;
// il n'est there que pour en tirer des faits d'identification.
type webAttempt struct {
	outcome string
	detail  string
	// cred est l'identifiant qui a fonctionne, ENTIER et non son seul ID : un
	// identifiant saisi pour un seul essai n'en a pas, et c'est pourtant lui que le
	// connecteur devra present  pour lire plus profond.
	cred Credential
	path string
	body string
}

// webChallenge : la page qui a repondu 401, et ce qu'elle reclame.
type webChallenge struct {
	base       string
	url        string
	scheme     string
	realm      string
	tried      []string
	lastStatus int
}

func webPortOpen(ports []int) bool {
	return hasPort(ports, 80) || hasPort(ports, 443) ||
		hasPort(ports, 8080) || hasPort(ports, 8443)
}

// webBases : les bases d'URL a essayer, HTTPS d'abord — quand les deux sont
// ouverts, le 80 d'un device redirige en general vers son 443.
func webBases(ip string, ports []int) []string {
	var out = []string{}
	if hasPort(ports, 443) || hasPort(ports, 8443) {
		out = append(out, "https://"+net.JoinHostPort(ip, itoa(httpsPort(ports))))
	}
	if hasPort(ports, 80) || hasPort(ports, 8080) {
		out = append(out, "http://"+net.JoinHostPort(ip, itoa(httpPort(ports))))
	}
	return out
}

// webSession : une session web de sonde, qui part par le same chemin que tout le
// reste (le tunnel du proxy, ou rien).
//
// MaxBody est reduit : verify un mot de passe se joue sur un code de statut, et
// les faits qu'on peche dans une page state tiennent dans ses premiers
// kilo-octets. Rapatrier deux megaoctets par adresse balayee n'apporterait rien
// et se paierait sur un lien lent.
func (p *Prober) webSession(base string) *driver.WebSession {
	var s = driver.NewWebSession(base, p.Dialer, webAuthWait)
	s.MaxBody = 128 * 1024
	return s
}

var webRealmRe = regexp.MustCompile(`(?i)realm\s*=\s*"?([^",]+)"?`)

// parseWebChallenge ne retient du `WWW-Authenticate` que de quoi le RACONTER :
// le schema et le realm. Le calcul de la response, lui, appartient a
// driver.WebSession — il n'y a pas deux implementations de Digest ici.
func parseWebChallenge(challenge string) (scheme, realm string) {
	var fields = strings.Fields(strings.TrimSpace(challenge))
	if len(fields) > 0 {
		scheme = fields[0]
	}
	var m = webRealmRe.FindStringSubmatch(challenge)
	if len(m) > 1 {
		realm = strings.TrimSpace(m[1])
	}
	return scheme, realm
}

// tryWeb establishes d'ABORD si l'interface authentifie au niveau HTTP, et essaie
// les identifiants ensuite. L'ordre n'est pas indifferent : c'est le passage
// anonyme qui permet de distinguer un refus d'une interface qui ne demande rien.
func (p *Prober) tryWeb(ctx context.Context, ip string, ports []int, creds []Credential, m Matcher) webAttempt {
	var eligible int
	var c Credential
	for _, c = range creds {
		if credFor(c, "http") {
			eligible++
		}
	}
	if eligible == 0 {
		// Dire l'absence rather que de rester muet : c'est la situation exacte de
		// l'exploitant qui a enregistre ses identifiants en « ssh » et voit sa
		// gamme web-only rester anonyme.
		return webAttempt{outcome: WebAuthNotTried,
			detail: "interface web non essayee : aucun identifiant de genre « http »" +
				" ou « tous » n'est enregistre."}
	}

	var paths = webAuthPaths[m.Driver]
	if len(paths) == 0 {
		paths = webRootOnly
	}
	var ch, err = p.findWebChallenge(ctx, webBases(ip, ports), paths)
	if err != nil {
		return webAttempt{outcome: WebAuthError,
			detail: "interface web injoignable pour l'essai d'identifiants : " +
				firstLine(err.Error())}
	}
	if ch.url == "" {
		return webAttempt{outcome: WebAuthNone, detail: fmt.Sprintf(
			"l'interface web ne demande aucune authentification HTTP sur %s"+
				" (response %d) : aucun identifiant n'a donc been presented. Soit elle"+
				" authentifie par formulaire — une session dans un cookie, que seul un"+
				" connecteur sait poser — soit ses pages de data sont ailleurs.",
			strings.Join(ch.tried, ", "), ch.lastStatus)}
	}

	var announced = strings.TrimSpace(ch.scheme + " realm « " + ch.realm + " »")
	if ch.realm == "" {
		announced = ch.scheme
	}
	var failures int
	for _, c = range creds {
		if ctx.Err() != nil {
			return webAttempt{outcome: WebAuthError, detail: ctx.Err().Error()}
		}
		if !credFor(c, "http") {
			continue
		}
		// La same borne que sur SSH, et pour la same raison : la politique AAA d'un
		// device compte les echecs toutes origines confondues, et decouvrir un
		// switch ne doit pas verrouiller le compte de l'equipe network.
		if failures >= maxAuthFailures {
			break
		}
		var session = p.webSession(ch.base)
		session.SetAuth(driver.AuthAuto, c.Username, c.Secret)
		var resp, getErr = session.Get(ctx, ch.url)
		if getErr != nil {
			// C'est ici qu'atterrit un schema que driver.WebSession ne sait pas
			// present  (NTLM, Bearer…). Ce n'est pas un mauvais mot de passe.
			return webAttempt{outcome: WebAuthError,
				detail: "essai d'authentification web interrompu : " + firstLine(getErr.Error())}
		}
		switch {
		case resp.Status == http.StatusUnauthorized:
			failures++
		case resp.Status < 400:
			return webAttempt{outcome: WebAuthOK, cred: c,
				path: ch.url, body: resp.Body,
				detail: fmt.Sprintf("authentification web accepted sur %s (%s)",
					ch.url, announced)}
		default:
			// 403, 500… : l'identifiant n'a pas been redemande, il n'est donc pas
			// refused — mais on ne sait pas dire qu'il est accepted non plus. On rend
			// le code tel quel rather que de trancher a la place de l'exploitant.
			return webAttempt{outcome: WebAuthError, path: ch.url,
				detail: fmt.Sprintf("response HTTP %d sur %s after presentation d'un"+
					" identifiant : ni acceptation, ni refus d'authentification.",
					resp.Status, ch.url)}
		}
	}
	return webAttempt{outcome: WebAuthRefused, path: ch.url, detail: fmt.Sprintf(
		"identifiants refused par l'interface web : %s redemande a s'authentifier"+
			" sur %s after %d essai(s). C'est bien un mot de passe a corriger.",
		announced, ch.url, failures)}
}

// isGenericDriver : « on n'a rien reconnu ». Les deux drivers de repli portent ce
// sens, et rien d'autre ne doit s'en approcher.
func isGenericDriver(name string) bool {
	return name == "" || name == genericSSH.Driver || name == genericHTTP.Driver
}

// identifyFromWeb cherche une plateforme dans le corps d'une page authentifiee.
// Driver vide = rien reconnu ; on ne renvoie deliberately PAS de driver
// generique, qui n'apprendrait rien et ecraserait le driver en place.
func identifyFromWeb(body string) Matcher {
	if strings.TrimSpace(body) == "" {
		return Matcher{}
	}
	var m Matcher
	for _, m = range matchers {
		if m.Re.MatchString(body) {
			return m
		}
	}
	return Matcher{}
}

// findWebChallenge cherche, ANONYMEMENT, une page qui reclame une
// authentification HTTP. Aucun identifiant n'est presented a ce stade : un 401 ne
// compte pour aucune politique de verrouillage, contrairement a un mot de passe
// faux.
func (p *Prober) findWebChallenge(ctx context.Context, bases, paths []string) (webChallenge, error) {
	var out = webChallenge{tried: []string{}}
	var lastErr error
	var base, path string
	for _, base = range bases {
		var session = p.webSession(base)
		var reached bool
		for _, path = range paths {
			var resp, err = session.Get(ctx, path)
			if err != nil {
				// La base ne repond pas (TLS refused, port filtre au-dela du dial) :
				// inutile de lui demander ses autres chemins.
				lastErr = err
				break
			}
			reached = true
			out.tried = append(out.tried, base+path)
			out.lastStatus = resp.Status
			if resp.Status == http.StatusUnauthorized {
				out.base, out.url = base, base+path
				out.scheme, out.realm = parseWebChallenge(
					http.Header(resp.Headers).Get("WWW-Authenticate"))
				return out, nil
			}
		}
		// Une base qui repond a repondu : on ne va pas cogner a la suivante. Le
		// balayage d'une plage se paie par adresse, et une interface web qui
		// authentifie sur son 80 mais pas sur son 443 n'existe pas dans la nature.
		if reached {
			return out, nil
		}
	}
	if len(out.tried) == 0 && lastErr != nil {
		return out, lastErr
	}
	return out, nil
}

func isAuthFailure(err error) bool {
	var s = strings.ToLower(err.Error())
	return strings.Contains(s, "unable to authenticate") ||
		strings.Contains(s, "no supported methods") ||
		strings.Contains(s, "permission denied")
}

func transportsOf(ports []int) []string {
	var out = []string{}
	if hasPort(ports, 22) {
		out = append(out, "ssh")
	}
	if hasPort(ports, 23) {
		out = append(out, "telnet")
	}
	if hasPort(ports, 80) || hasPort(ports, 8080) {
		out = append(out, "http")
	}
	if hasPort(ports, 443) || hasPort(ports, 8443) {
		out = append(out, "https")
	}
	return out
}

func hasPort(ports []int, want int) bool {
	var p int
	for _, p = range ports {
		if p == want {
			return true
		}
	}
	return false
}

func httpsPort(ports []int) int {
	if hasPort(ports, 443) {
		return 443
	}
	return 8443
}

func httpPort(ports []int) int {
	if hasPort(ports, 80) {
		return 80
	}
	return 8080
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func firstLine(s string) string {
	var i = strings.IndexByte(s, '\n')
	if i < 0 {
		return s
	}
	return s[:i]
}

// sanitize retire les sequences de control : les bannieres et les shells de
// switchs sont truffes d'echappements ANSI qui pollueraient l'UI et la base.
func sanitize(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	var b strings.Builder
	var r rune
	for _, r = range s {
		if r == '\n' || r == '\t' || r >= 0x20 {
			b.WriteRune(r)
		}
	}
	return strings.ReplaceAll(b.String(), "\r", "")
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// enrich laisse le connecteur complete l'identification.
//
// Un enrichissement impossible ne fait PAS echouer une discovery qui a par
// ailleurs reussi — mais il laisse une trace dans les notes. Un failure muet est
// indiagnosticable : on ne saurait pas distinguer « le connecteur n'a rien a
// dire » de « le connecteur n'a pas pu parler a the device ».
// enrich laisse le connecteur reconnu preciser ce que la sonde generique ne sait
// pas voir.
//
// `cred` est l'identifiant qui a ACTUALLY ouvert la porte, et `haveCred` dit
// s'il y en a un. Aucun autre essai n'est tente ici : celui-there a fonctionne, et en
// rejouer d'autres ne ferait que remplir le journal d'authentification de
// the device et ronger la protection contre le verrouillage de compte que la
// sonde vient d'appliquer.
func (p *Prober) enrich(ctx context.Context, res *Result, cred Credential, haveCred bool) {
	switch res.Driver {
	case "aruba-instant-on-1930":
		p.enrichInstantOn(ctx, res)
	case "mikrotik-swos":
		p.enrichSwOS(ctx, res, cred)
	case "mikrotik-routeros":
		p.enrichRouterOS(ctx, res, cred, haveCred)
	}
}

// enrichSwOS : le model EXACT sans le moindre identifiant.
//
// C'est la particularite de cette plateforme, et elle vaut d'be exploitee : le
// realm de son challenge Digest porte la reference du produit
// (`realm="CSS326-24G-2S+"`), servi avec le 401 lui-same. L'enrichissement a donc
// un sens same sur un device dont personne ne connait le mot de passe — ce qui
// est exactement la situation d'un balayage de discovery.
//
// Identity PARTIELLE est retenue AVANT le test d'erreur, et l'ordre n'est pas
// negociable : Inspect rend ce qu'il a pu establish en same temps que la raison de
// son arret, et « c'est un CSS326-24G-2S+ dont on n'a pas le mot de passe » est un
// result utile there where « failure » n'en est pas un.
func (p *Prober) enrichSwOS(ctx context.Context, res *Result, cred Credential) {
	var id, err = swos.Inspect(ctx, res.IP, cred.Username, cred.Secret, p.Dialer)
	if id.Model != "" {
		res.Model = id.Model
		res.PortGroups = id.PortGroups()
		if res.Confidence < ConfDeviceSelfReport {
			res.Confidence = ConfDeviceSelfReport
			res.Evidence = "identification par the device (" + id.Source + ")"
		}
	}
	if id.Serial != "" {
		res.Serial = id.Serial
	}
	if id.MAC != "" {
		res.MAC = id.MAC
	}
	if res.Hostname == "" && id.Hostname != "" {
		res.Hostname = id.Hostname
	}
	if id.Firmware != "" {
		res.Notes = strings.TrimSpace(res.Notes + " SwOS " + id.Firmware)
	}
	if err != nil {
		res.Notes = strings.TrimSpace(res.Notes +
			" identification partielle : " + firstLine(err.Error()))
	}
}

// enrichRouterOS : ici, rien sans identifiant.
//
// L'API REST repond 401 sur toutes ses ressources, y compris celle qui porte le
// model. C'est la difference de nature avec les deux autres gammes, et elle se
// dit rather qu'elle ne se contourne : sans identifiant qui ait fonctionne, la
// discovery en reste au titre de la page web — ce qui suffit a ranger
// the device dans le bon connecteur, mais pas a nommer son model.
//
// Le message est written pour l'exploitant qui regarde la file des decouvertes et se
// demande pourquoi cette ligne est moins renseignee que les autres.
func (p *Prober) enrichRouterOS(ctx context.Context, res *Result, cred Credential, haveCred bool) {
	if !haveCred {
		res.Notes = strings.TrimSpace(res.Notes +
			" Modele et serial number non lus : l'API REST de RouterOS les reserve " +
			"a une session authentifiee, et aucun identifiant n'a ouvert cet device.")
		return
	}
	var id, err = routeros.Inspect(ctx, res.IP, p.Dialer, cred.Username, cred.Secret)
	if err != nil {
		res.Notes = strings.TrimSpace(res.Notes +
			" identification approfondie impossible : " + firstLine(err.Error()))
		return
	}
	if id.Model != "" {
		res.Model = id.Model
	}
	if id.Serial != "" {
		res.Serial = id.Serial
	}
	if id.MAC != "" {
		res.MAC = id.MAC
	}
	if res.Hostname == "" && id.Hostname != "" {
		res.Hostname = id.Hostname
	}
	res.PortGroups = id.PortGroups()
	if res.Confidence < ConfDeviceSelfReport {
		res.Confidence = ConfDeviceSelfReport
		res.Evidence = "identification par the device (" + id.Source + ")"
	}
	// Deux versions, et les confondre ferait croire a un device quatre
	// versions majeures en retard : RouterOS est le SYSTEME, RouterBOOT le
	// chargeur boot.
	if id.Firmware != "" {
		var note = " RouterOS " + id.Firmware
		if id.RouterBoot != "" {
			note += " (RouterBOOT " + id.RouterBoot + ")"
		}
		res.Notes = strings.TrimSpace(res.Notes + note)
	}
}

func (p *Prober) enrichInstantOn(ctx context.Context, res *Result) {
	var id, err = instanton.Inspect(ctx, res.IP, p.Dialer)
	if err != nil {
		res.Notes = strings.TrimSpace(res.Notes +
			" identification approfondie impossible : " + firstLine(err.Error()))
		return
	}
	if id.Model != "" {
		res.Model = id.Model
	}
	if id.Serial != "" {
		res.Serial = id.Serial
	}
	if id.MAC != "" {
		res.MAC = id.MAC
	}
	if res.Hostname == "" && id.Hostname != "" {
		res.Hostname = id.Hostname
	}
	// `PortGroups` est un CHAMP ici, et une METHOD sur les deux connecteurs
	// MikroTik. Ce n'est pas une incoherence a reparer : identity de cette gamme
	// est now celle du contrat partage (`driver.Identity`), where la front panel est
	// une data, tandis que les deux autres gardent un type identity plus riche
	// que le contrat — et Go interdit d'attacher une method au type d'un autre
	// paquet. Aligner les trois voudrait donc dire appauvrir deux connecteurs.
	res.PortGroups = id.PortGroups
	// Une identification par le constructeur lui-same vaut mieux qu'un titre
	// HTML : la confiance monte en consequence.
	if res.Confidence < ConfDeviceSelfReport {
		res.Confidence = ConfDeviceSelfReport
		res.Evidence = "identification par the device (" + id.Source + ")"
	}
	if id.Firmware != "" {
		res.Notes = strings.TrimSpace(res.Notes + " firmware " + id.Firmware +
			" (build " + id.Build + ")")
	}
}

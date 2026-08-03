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
	// Connectors also register themselves with the driver registry through
	// their init: discovery is the point in the program that consults all of
	// them, so this is where they come on stage.
	"meshcraft/internal/driver/cisco/ios"
	"meshcraft/internal/driver/fs/s3900"
	_ "meshcraft/internal/driver/hpe/procurve" // registers "hpe-procurve" via init()
	"meshcraft/internal/driver/mikrotik/routeros"
	"meshcraft/internal/driver/mikrotik/swos"
)

// Ports probed: the ones that distinguish a manageable network device.
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

// Credential is a credential to try. Secret = plaintext password, decrypted
// just before the attempt and never logged.
type Credential struct {
	ID   string
	Name string
	// Kind: the transport on which this credential makes sense.
	//
	// "ssh" and "http" are tried, "telnet" is not (see the note of the same
	// name further below). "any" applies to every transport tried, and EMPTY
	// does the same: that's the case of the one-off credential entered for a
	// single attempt, where the operator shouldn't have to guess which way
	// meshcraft will get in — they give a username/password pair, and we try
	// it on every path we know how to take.
	Kind     string // ssh | telnet | http | any | "" (all)
	Username string
	Secret   string
}

// credFor says whether a credential should be tried on this transport.
func credFor(c Credential, transport string) bool {
	switch c.Kind {
	case "", "any":
		return true
	default:
		return c.Kind == transport
	}
}

// countKind counts the credentials of a given kind, as it was DECLARED: here
// we do NOT want credFor's broadening, but rather "how many credentials were
// registered for this specific transport".
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

// Result gathers everything learned about an address.
type Result struct {
	IP         string   `json:"ip"`
	Reachable  bool     `json:"reachable"`
	LatencyMS  int      `json:"latency_ms"`
	OpenPorts  []int    `json:"open_ports"`
	Transports []string `json:"transports"`

	SSHBanner  string `json:"ssh_banner,omitempty"`
	HTTPServer string `json:"http_server,omitempty"`
	HTTPTitle  string `json:"http_title,omitempty"`

	// Populated only if a credential worked.
	LoginOK        bool   `json:"login_ok"`
	CredentialID   string `json:"credential_id,omitempty"`
	CredentialName string `json:"credential_name,omitempty"`
	ShellOutput    string `json:"shell_output,omitempty"`
	RawConfig      string `json:"raw_config,omitempty"`

	// Outcome of trying credentials against the web interface, and its
	// explanatory sentence.
	//
	// This field exists because "not authenticated" covers three situations
	// that call for entirely different operator actions, see the WebAuth*
	// constants. Without it, the UI could only show a failure, and the only
	// action a failure suggests is to look for a password — including in
	// cases where no password was ever presented to anyone.
	WebAuth       string `json:"web_auth,omitempty"`
	WebAuthDetail string `json:"web_auth_detail,omitempty"`

	// MAC and PortGroups come from the connector when it knows how to read them.
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

// Dialer opens the probe's TCP connections. This is the tunnel's ingress
// point: `net.Dialer` for the "Local" proxy (the instance itself), the
// tunnel hub for a remote proxy. The probe itself doesn't know which one it
// is using — that's the "single code path" rule (docs/01-Architecture/Vue
// d'ensemble).
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Prober probes an address. Stateless: safe under concurrency.
type Prober struct {
	// Empty Dialer = direct connections from the controller.
	Dialer Dialer

	// FetchConfig: also read the current configuration (slower, and it
	// leaves a trace in the device's logs). Used for manual add and
	// configuration import, not for mass scanning.
	FetchConfig bool
	// ForceDriver constrains the driver (type chosen by the user).
	ForceDriver string
}

// Probe runs the full sequence against an address.
func (p *Prober) Probe(ctx context.Context, ip string, creds []Credential) Result {
	var res = Result{IP: ip, OpenPorts: []int{}, Transports: []string{}}

	var started = time.Now()
	// Ports are probed IN PARALLEL: run in series, a dead host used to cost
	// 6 × 900 ms, which put a /16 out of reach (hours). In parallel it costs
	// 900 ms, and scanning a /16 becomes a matter of minutes again.
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
		res.Notes = strings.TrimSpace(res.Notes + " Telnet open: identification by banner only.")
	}
	if hasPort(res.OpenPorts, 443) || hasPort(res.OpenPorts, 8443) {
		res.HTTPServer, res.HTTPTitle = p.grabHTTP(ctx, ip, httpsPort(res.OpenPorts), true)
	}
	if res.HTTPServer == "" && res.HTTPTitle == "" &&
		(hasPort(res.OpenPorts, 80) || hasPort(res.OpenPorts, 8080)) {
		res.HTTPServer, res.HTTPTitle = p.grabHTTP(ctx, ip, httpPort(res.OpenPorts), false)
	}

	// First identification, without authentication.
	var m, conf, evidence = Identify("", res.SSHBanner, res.HTTPServer, res.HTTPTitle)
	if p.ForceDriver != "" {
		m = MatcherFor(p.ForceDriver)
		evidence = "type forced by the user"
		conf = ConfShellOutput
	}

	// winner is the credential that opened the door, kept in clear text for
	// the duration of the probe so it can be HANDED to the connector that
	// will read deeper (see enrich). It never leaves this function any other
	// way: not in the Result, not in a note, not in a log.
	var winner Credential
	var haveWinner bool

	// Try credentials over SSH, then reinforced identification.
	if hasPort(res.OpenPorts, 22) && len(creds) > 0 {
		var out, cred, err = p.trySSH(ctx, ip, creds, m)
		if cred.Username != "" {
			// Kept because the session opened, even if the shell answered
			// nothing: this is the case of RouterOS, whose REST API can be
			// read with this very credential even though its shell stays silent.
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

	// Try credentials against the web interface, then reinforced identification.
	//
	// After SSH, and only if SSH opened nothing: a device that has already
	// talked through its CLI has nothing to gain from a second attempt, and
	// every attempt leaves a line in the device's authentication log. That
	// doesn't make the web path a luxury, though — it's the ONLY one on
	// web-only ranges, and the only one left when the SSH probe times out
	// while the same device's REST API responds.
	if !res.LoginOK && len(creds) > 0 && webPortOpen(res.OpenPorts) {
		var att = p.tryWeb(ctx, ip, res.OpenPorts, creds, m)
		res.WebAuth, res.WebAuthDetail = att.outcome, att.detail
		if att.outcome != WebAuthOK && att.detail != "" {
			// An inconclusive outcome must SURVIVE the probe result: the
			// note goes into the candidate, and that's where it will be read
			// again in three days.
			res.Notes = strings.TrimSpace(res.Notes + " " + att.detail)
		}
		if att.outcome == WebAuthOK {
			res.LoginOK = true
			res.CredentialID = att.cred.ID
			res.CredentialName = att.cred.Name
			winner, haveWinner = att.cred, true

			// What the authenticated page reveals, and which wasn't
			// accessible to an anonymous visitor. We never replace a fact
			// already known: this one comes from a generic read, that one
			// may come from a connector.
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
			// The authenticated page is only entrusted with identification
			// if nothing else succeeded at it. It's rich text — a device's
			// UI can name a different vendor in passing, in a compatibility
			// note — and letting a page body undo a recognition obtained
			// from the SSH banner would be a regression, not progress.
			if p.ForceDriver == "" && isGenericDriver(m.Driver) {
				var wm = identifyFromWeb(att.body)
				if wm.Driver != "" {
					m, facts = wm, true
				}
			}
			// Confidence rises, but only if authentication reported
			// something: succeeding at connecting doesn't prove we
			// identified the device better, and confidence that rises
			// without a new fact is a quiet lie. When there is indeed a new
			// fact, it deserves ConfDeviceSelfReport: it's the device
			// talking about itself, behind an authentication it demanded
			// itself — not a regex on an HTML title.
			if facts && conf < ConfDeviceSelfReport {
				conf = ConfDeviceSelfReport
				evidence = "authenticated device page (" + att.path + ")"
			}
			// A configuration that was requested but not read must be
			// SAID. The operator checked "also read the configuration" and
			// authentication succeeded: without this sentence, they would
			// conclude the device refuses to hand over its configuration,
			// when actually it's meshcraft that doesn't yet know how to
			// read it through this path — exporting a web-only range
			// belongs to its connector, not to a generic probe.
			if p.FetchConfig && res.RawConfig == "" {
				res.Notes = strings.TrimSpace(res.Notes +
					" Configuration not read: access was through the web interface," +
					" whose export belongs to the range's connector.")
			}
		}
	}

	// The "telnet" kind is NOT left in silence: a registered credential that
	// we never try is exactly the bug we just fixed for the web path. We say
	// so here, on the device where the question actually arises — the one
	// whose port 23 is open.
	if hasPort(res.OpenPorts, 23) {
		var n = countKind(creds, "telnet")
		if n > 0 {
			res.Notes = strings.TrimSpace(res.Notes + fmt.Sprintf(
				" %d telnet-kind credential(s) registered: NOT attempted,"+
					" meshcraft has no telnet client.", n))
		}
	}

	res.Driver = m.Driver
	res.Vendor = m.Vendor
	res.Confidence = conf
	res.Evidence = evidence

	// A recognized connector has the right to refine what the generic probe
	// can't see: exact model, serial number, port front panel. That's
	// platform knowledge, it has no place in the probe.
	p.enrich(ctx, &res, winner, haveWinner)
	if res.Hostname == "" && res.HTTPTitle != "" {
		res.Hostname = ""
	}
	return res
}

// scanPorts probes every port in parallel and returns the ones that respond,
// in ProbePorts order (a stable result is easier to compare).
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

// dial opens a connection, through the chosen proxy's tunnel or directly.
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

// grabBanner reads the banner the server sends SPONTANEOUSLY (the case for
// SSH and many Telnet servers). No authentication attempt here: it's free
// and carries no risk of account lockout.
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
	// InsecureSkipVerify: network devices present self-signed certificates,
	// that's the norm. We're only doing identification here.
	var client = &http.Client{
		Timeout: httpTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			// HTTP goes through the same path as everything else: this is
			// what makes it possible to reach a device's web UI through a proxy.
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

// sshClientConfig deliberately accepts old algorithms: without them, a good
// portion of the real fleet (switches more than five years old) is
// unreachable. The host key isn't verified — during discovery we don't know
// it yet.
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
			// FS S3900 OpCode 1.7 vintage: KEX offer is group1/group14-sha1 only
			// (already listed above) and the host key can be DSA.
			"ssh-dss",
		},
	}
}

// maxAuthFailures bounds the attempts per host: beyond that, we stop even if
// there are still credentials left to test. AAA policies lock accounts after
// a few failures — discovering a switch must not block the network team's
// access (see docs/01-Architecture/Discovery, safeguards).
const maxAuthFailures = 2

// trySSH tries the credentials in the order received and, on the first one
// that succeeds, runs the driver's identification commands.
// trySSH returns the credential that worked, and not just its base
// identifier: a credential entered for a single attempt has NO ID (nothing
// registers it), and finding it again afterward in the list by its ID would
// be impossible. Yet that is precisely the one an operator uses when adding
// a device by hand — the case where enrichment matters most.
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
				"credential attempts stopped after %d failures (account-lockout protection)",
				failures)
		}
		var client, dialErr = p.sshDial(ctx, ip, c)
		if dialErr != nil {
			if isAuthFailure(dialErr) {
				failures++
				continue
			}
			return "", Credential{}, fmt.Errorf("SSH: %v", firstLine(dialErr.Error()))
		}
		var text, shellErr = runShell(ctx, client, p.commandsFor(m))
		_ = client.Close()
		if shellErr != nil && text == "" {
			// The credential is returned DESPITE the error: the session
			// opened, so the password is correct — it's the shell that
			// didn't answer as expected. This is exactly the case of
			// RouterOS, whose interactive shell can't be scraped, and this
			// refusal to converse must not cost us a credential that
			// otherwise works.
			return "", c, fmt.Errorf("SSH session: %v", firstLine(shellErr.Error()))
		}
		return text, c, nil
	}
	if failures > 0 {
		return "", Credential{}, fmt.Errorf("no standard credential accepted (%d refused)", failures)
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

// Markers framing the configuration in the shell output. These are
// deliberately invalid commands: the device rejects them, but their echo in
// the output cleanly delimits the configuration block.
const (
	configMarkerBegin = "echo MESHCRAFT-CONFIG-BEGIN"
	configMarkerEnd   = "echo MESHCRAFT-CONFIG-END"
)

// runShell opens an interactive shell (many switches refuse `exec`), sends
// the commands, and reads the output for a bounded time.
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

	// A wide pty avoids pagination on platforms that ignore the disable command.
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
	// "exit" closes cleanly, which unblocks reading without waiting out the
	// whole budget.
	_, _ = io.WriteString(stdin, "exit\n")
	_ = stdin.Close()

	var budget = time.NewTimer(shellBudget)
	defer budget.Stop()
	var text string
	select {
	case text = <-collected:
		return sanitize(text), nil
	case <-budget.C:
		return "", fmt.Errorf("timed out after %s", shellBudget)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// extractConfig isolates the configuration block between the markers. In
// the absence of markers, returns the whole output: the import parser knows
// how to ignore the noise, and a truncated configuration would be worse
// than a noisy one.
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

// --- Trying credentials against the web interface ---
//
// What this attempt covers: authentication AT THE HTTP LEVEL, the kind that
// shows up in an `Authorization` header because the device answered 401
// while itself announcing the scheme it wants (driver.AuthAuto — we never
// guess it, see internal/driver/webfetch.go). Two ranges in the test fleet
// have nothing else: a SwOS only opens port 80 and serves its data pages
// over Digest, and RouterOS's REST API answers over Basic on a device whose
// SSH probe times out. Before this attempt, discovery would see them
// without ever being able to introduce itself.
//
// What this attempt does NOT cover, and will never be able to cover:
// FORM-based UIs. An Aruba Instant On returns 200 to an anonymous visitor
// and only opens a session after a POST of a form whose URL, fields, and
// cookie-handling method must be known. None of that can be guessed from a
// generic probe: it's platform knowledge, hence a connector's job (see
// docs/01-Architecture/Connecteurs). This is exactly the reason the third
// outcome below exists — without it, this limitation would read like a
// refused password.
const (
	// WebAuthNotTried: attempt not made — no web port, no usable web
	// credential, or SSH had already opened a session.
	WebAuthNotTried = ""
	// WebAuthOK: the device accepted a credential.
	WebAuthOK = "ok"
	// WebAuthRefused: the device asked AGAIN to authenticate after seeing
	// our credentials. Here, and only here, is there a password to fix.
	WebAuthRefused = "refused"
	// WebAuthNone: none of the pages tried request HTTP authentication.
	// This is NOT a credential failure — none was ever presented to
	// anyone. Confusing the two means sending someone to look for a wrong
	// password for hours on an interface that never expected one.
	WebAuthNone = "no-http-auth"
	// WebAuthError: the attempt didn't complete (link down, TLS refused,
	// unknown authentication scheme, response we don't know how to
	// interpret). Neither a refusal nor an absence: a failed attempt, to be
	// retried.
	WebAuthError = "error"
)

// webAuthPaths says WHERE to look for a 401 on an already-recognized
// platform.
//
// The root isn't enough, and this was verified on both test devices: SwOS
// and RouterOS both serve their home page with a 200 to an anonymous
// visitor, and only require authentication on their DATA pages. Sticking to
// "/" would therefore have concluded "this interface doesn't authenticate"
// on both devices this work is meant to unblock.
//
// This table is the bare minimum, and it has the same weakness as the
// patterns in fingerprint.go: it's platform knowledge tucked into a generic
// probe. Its rightful home is the connector, which already knows where its
// pages live; in the meantime, having it here is the difference between a
// credential that gets tried and a decorative one.
var webAuthPaths = map[string][]string{
	// SwOS's `*.b` pages: Digest, realm = the switch's model.
	"mikrotik-swos": {"/sys.b"},
	// RouterOS's REST API: Basic. `system/resource` is the least
	// committal resource that requires authentication.
	"mikrotik-routeros": {"/rest/system/resource"},
}

// webRootOnly: in the absence of a recognized platform, the root and
// nothing else. Guessing paths on an unknown interface would mean scanning
// the device blindly — a lot of noise in its logs for a gamble.
var webRootOnly = []string{"/"}

// webAttempt: what we report about a web attempt. `body` stays local to the
// probe; it's only there to extract identification facts from.
type webAttempt struct {
	outcome string
	detail  string
	// cred is the credential that worked, the WHOLE thing and not just its
	// ID: a credential entered for a single attempt doesn't have one, and
	// yet it's exactly the one the connector will need to present in order
	// to read deeper.
	cred Credential
	path string
	body string
}

// webChallenge: the page that answered 401, and what it demands.
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

// webBases: the URL bases to try, HTTPS first — when both are open, a
// device's port 80 generally redirects to its 443.
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

// webSession: a probe web session, which goes through the same path as
// everything else (the proxy's tunnel, or nothing).
//
// MaxBody is kept small: checking a password comes down to a status code,
// and the facts we fish out of a static page fit in its first few
// kilobytes. Fetching two megabytes per scanned address wouldn't help and
// would cost dearly on a slow link.
func (p *Prober) webSession(base string) *driver.WebSession {
	var s = driver.NewWebSession(base, p.Dialer, webAuthWait)
	s.MaxBody = 128 * 1024
	return s
}

var webRealmRe = regexp.MustCompile(`(?i)realm\s*=\s*"?([^",]+)"?`)

// parseWebChallenge keeps from `WWW-Authenticate` only enough to REPORT it:
// the scheme and the realm. Computing the response itself belongs to
// driver.WebSession — there's no second implementation of Digest here.
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

// tryWeb FIRST establishes whether the interface authenticates at the HTTP
// level, and only tries the credentials afterward. The order isn't
// arbitrary: it's the anonymous pass that lets us distinguish a refusal from
// an interface that asks for nothing.
func (p *Prober) tryWeb(ctx context.Context, ip string, ports []int, creds []Credential, m Matcher) webAttempt {
	var eligible int
	var c Credential
	for _, c = range creds {
		if credFor(c, "http") {
			eligible++
		}
	}
	if eligible == 0 {
		// State the absence rather than staying silent: this is exactly the
		// situation of the operator who registered their credentials as
		// "ssh" and sees their web-only range stay anonymous.
		return webAttempt{outcome: WebAuthNotTried,
			detail: "web interface not tried: no credential of kind \"http\"" +
				" or \"any\" is registered."}
	}

	var paths = webAuthPaths[m.Driver]
	if len(paths) == 0 {
		paths = webRootOnly
	}
	var ch, err = p.findWebChallenge(ctx, webBases(ip, ports), paths)
	if err != nil {
		return webAttempt{outcome: WebAuthError,
			detail: "web interface unreachable for the credential attempt: " +
				firstLine(err.Error())}
	}
	if ch.url == "" {
		return webAttempt{outcome: WebAuthNone, detail: fmt.Sprintf(
			"the web interface requests no HTTP authentication on %s"+
				" (response %d): so no credential was presented. Either it"+
				" authenticates via a form — a session in a cookie, which only a"+
				" connector knows how to set up — or its data pages are elsewhere.",
			strings.Join(ch.tried, ", "), ch.lastStatus)}
	}

	var announced = strings.TrimSpace(ch.scheme + " realm \"" + ch.realm + "\"")
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
		// The same bound as on SSH, and for the same reason: a device's AAA
		// policy counts failures across all origins, and discovering a
		// switch must not lock the network team's account.
		if failures >= maxAuthFailures {
			break
		}
		var session = p.webSession(ch.base)
		session.SetAuth(driver.AuthAuto, c.Username, c.Secret)
		var resp, getErr = session.Get(ctx, ch.url)
		if getErr != nil {
			// This is where a scheme that driver.WebSession doesn't know how
			// to present (NTLM, Bearer...) lands. This is not a bad password.
			return webAttempt{outcome: WebAuthError,
				detail: "web authentication attempt interrupted: " + firstLine(getErr.Error())}
		}
		switch {
		case resp.Status == http.StatusUnauthorized:
			failures++
		case resp.Status < 400:
			return webAttempt{outcome: WebAuthOK, cred: c,
				path: ch.url, body: resp.Body,
				detail: fmt.Sprintf("web authentication accepted on %s (%s)",
					ch.url, announced)}
		default:
			// 403, 500...: the credential wasn't asked for again, so it
			// isn't refused — but we can't say it was accepted either. We
			// return the code as-is rather than deciding in the operator's
			// place.
			return webAttempt{outcome: WebAuthError, path: ch.url,
				detail: fmt.Sprintf("HTTP response %d on %s after presenting a"+
					" credential: neither acceptance nor refusal of authentication.",
					resp.Status, ch.url)}
		}
	}
	return webAttempt{outcome: WebAuthRefused, path: ch.url, detail: fmt.Sprintf(
		"credentials refused by the web interface: %s asked again to authenticate"+
			" on %s after %d attempt(s). This is indeed a password to fix.",
		announced, ch.url, failures)}
}

// isGenericDriver: "nothing was recognized". The two fallback drivers carry
// this meaning, and nothing else should come close to it.
func isGenericDriver(name string) bool {
	return name == "" || name == genericSSH.Driver || name == genericHTTP.Driver
}

// identifyFromWeb looks for a platform in the body of an authenticated
// page. Empty driver = nothing recognized; we deliberately do NOT return a
// generic driver, which would learn nothing and would overwrite the driver
// already in place.
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

// findWebChallenge looks, ANONYMOUSLY, for a page that requests HTTP
// authentication. No credential is presented at this stage: a 401 doesn't
// count against any lockout policy, unlike a wrong password.
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
				// The base doesn't respond (TLS refused, port filtered
				// beyond the dial): no point asking it for its other paths.
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
		// A base that responded, responded: we won't go knock on the next
		// one. Scanning a range is paid for per address, and a web
		// interface that authenticates on its 80 but not on its 443 doesn't
		// exist in the wild.
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

// sanitize strips control sequences: switch banners and shells are riddled
// with ANSI escapes that would pollute the UI and the database.
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

// enrich lets the connector complete the identification.
//
// An enrichment failure does NOT make a discovery that otherwise succeeded
// fail — but it leaves a trace in the notes. A silent failure is
// undiagnosable: there would be no way to distinguish "the connector has
// nothing to say" from "the connector couldn't talk to the device".
// enrich lets the recognized connector refine what the generic probe can't
// see.
//
// `cred` is the credential that ACTUALLY opened the door, and `haveCred`
// says whether there is one. No other attempt is made here: this one
// worked, and replaying others would only fill up the device's
// authentication log and eat into the account-lockout protection the probe
// just applied.
func (p *Prober) enrich(ctx context.Context, res *Result, cred Credential, haveCred bool) {
	switch res.Driver {
	case "aruba-instant-on-1930":
		p.enrichInstantOn(ctx, res)
	case "mikrotik-swos":
		p.enrichSwOS(ctx, res, cred)
	case "mikrotik-routeros":
		p.enrichRouterOS(ctx, res, cred, haveCred)
	case "fs-s3900":
		p.enrichS3900(ctx, res, cred, haveCred)
	case "cisco-ios":
		p.enrichCiscoIOS(ctx, res, cred, haveCred)
	}
}

// enrichCiscoIOS: model, serial and above all the FACEPLATE, read by the SSH
// connector with the one credential that already opened the door.
//
// The faceplate matters more on this vendor than on any other in the fleet.
// The 2960-X range alone ships 24- and 48-port variants, PoE and non-PoE, with
// four 1G cages OR two 10G cages — and only the part-number suffix tells them
// apart. Without this read, a Catalyst adopted by push would carry no drawable
// port, LLDP resolution would discard every neighbour, and the SNMP poller's
// MaxIfIndex bound would stay at zero. That exact sequence is what an S3900
// went through the day before its connector shipped.
func (p *Prober) enrichCiscoIOS(ctx context.Context, res *Result, cred Credential, haveCred bool) {
	if !haveCred {
		res.Notes = strings.TrimSpace(res.Notes +
			" Serial and faceplate not read: the CLI sits behind SSH authentication and no credential opened it.")
		return
	}
	var id, err = ios.Inspect(ctx, res.IP, p.Dialer, cred.Username, cred.Secret)
	if err != nil {
		res.Notes = strings.TrimSpace(res.Notes +
			" Deep identification failed: " + firstLine(err.Error()))
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
	if len(id.PortGroups) > 0 {
		res.PortGroups = id.PortGroups
	}
	if res.Confidence < ConfDeviceSelfReport {
		res.Confidence = ConfDeviceSelfReport
		res.Evidence = "identification by the device (" + id.Source + ")"
	}
	if id.Firmware != "" {
		res.Notes = strings.TrimSpace(res.Notes + " Cisco IOS " + id.Firmware + ".")
	}
}

// enrichS3900 : série, firmware et FAÇADE lus par le connecteur SSH, avec le
// seul identifiant qui a déjà ouvert la porte. Sans façade, un S3900 adopté en
// mode « push » n'aurait aucun port dessinable, la résolution LLDP jetterait
// chaque voisin et la borne MaxIfIndex du polling SNMP resterait à zéro.
func (p *Prober) enrichS3900(ctx context.Context, res *Result, cred Credential, haveCred bool) {
	if !haveCred {
		res.Notes = strings.TrimSpace(res.Notes +
			" Serie et facade non lues : le CLI est derriere l'authentification SSH et aucun identifiant n'a ouvert.")
		return
	}
	var id, err = s3900.Inspect(ctx, res.IP, p.Dialer, cred.Username, cred.Secret)
	if err != nil {
		res.Notes = strings.TrimSpace(res.Notes +
			" Identification approfondie impossible : " + firstLine(err.Error()))
		return
	}
	if id.Serial != "" {
		res.Serial = id.Serial
	}
	if len(id.PortGroups) > 0 {
		res.PortGroups = id.PortGroups
	}
	if res.Confidence < ConfDeviceSelfReport {
		res.Confidence = ConfDeviceSelfReport
		res.Evidence = "identification par the device (" + id.Source + ")"
	}
	if id.Firmware != "" {
		res.Notes = strings.TrimSpace(res.Notes + " FSOS OpCode " + id.Firmware + ".")
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

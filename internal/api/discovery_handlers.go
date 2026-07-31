package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/auth"
	"meshcraft/internal/confimport"
	"meshcraft/internal/discovery"
	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/store"
	"meshcraft/internal/tunnel"
)

// --- Permissions ---
//
// Standard credentials are instance data: global-admin.
//
// Scan ranges belong to a proxy: declaring them requires rights on the proxy
// ITSELF — site_admin on all sites it covers, or global-admin. On a proxy covering
// sites A and B, an administrator of site A alone could otherwise scan B's network.
//
// Adoption requires "operator" on the CHOSEN site — it alone brings a device into
// a site (cf. docs/03-Security/RBAC).

func (s *Server) canAdminProxy(r *http.Request, proxyID string) (bool, error) {
	var p, err = s.Store.GetProxy(r.Context(), proxyID)
	if err != nil {
		return false, err
	}
	return s.canAdminWholeProxy(r, p), nil
}

// --- Known standard credentials ---

func (s *Server) handleListKnownCredentials(w http.ResponseWriter, r *http.Request) {
	var creds, err = s.Store.ListKnownCredentials(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{
		"credentials": creds,
		// UI says where the key lives: a secret "encrypted with a key stored
		// alongside" must not be presented as protected.
		"key_external": s.Secrets != nil && s.Secrets.FromEnv,
		// Credentials whose secret no longer decrypts. The secret still does not leave:
		// only its UNREADABILITY does, which is an operational fact not a secret.
		"unreadable_secrets": s.unreadableCredentials(r.Context()),
	})
}

// unreadableCredentials lists credentials whose secret no longer opens with the
// current instance key.
//
// Why this screen is the ONLY place it can show: discovery silently skips them
// (cf. discoveryCredentials) — and rightly does not fail entirely for one credential
// in five. But the result is a row in the database, displayed like a normal credential,
// secret "stored" included, never tried. Without this signal, the operator concludes
// discovery is broken where they simply need to re-enter a password.
//
// Key fingerprint (migration 0024) makes the case RARE, not impossible: it prevents
// starting with another key, it does not repair rows sealed before it existed, or
// imported from another instance.
//
// A database read error does not fail the screen: return an empty list, the screen
// shows credentials, and nobody is blocked for an accessory diagnostic.
func (s *Server) unreadableCredentials(ctx context.Context) []string {
	var out = []string{}
	if s.Secrets == nil {
		return out
	}
	var all, err = s.Store.CredentialSecrets(ctx)
	if err != nil {
		return out
	}
	var e store.EncCredential
	for _, e = range all {
		// An EMPTY ciphertext is not unreadable ciphertext: it is a credential without
		// secret, which the list already says elsewhere (has_secret).
		if len(e.SecretEnc) == 0 {
			continue
		}
		var _, openErr = s.Secrets.Open(e.SecretEnc)
		if openErr != nil {
			out = append(out, e.ID)
		}
	}
	return out
}

type knownCredInput struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
	Priority int    `json:"priority"`
	Enabled  bool   `json:"enabled"`
}

// credKinds: kinds a standard credential may carry.
//
// "any" was added because kind silently decided what would be tried: an operator
// storing a password as "ssh" saw their web-only line stay anonymous with no warning.
// Many devices accept the same pair on both transports — making them enter it twice
// would be absurd.
//
// Rows already in the database keep exact meaning: "ssh" stays "SSH only". No
// migration, the column is free text.
var credKinds = map[string]bool{"ssh": true, "telnet": true, "http": true, "any": true}

func (in *knownCredInput) normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Username = strings.TrimSpace(in.Username)
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	if in.Kind == "" {
		in.Kind = "ssh"
	}
	if in.Priority == 0 {
		in.Priority = 100
	}
}

// checkKind rejects an unknown kind. Not validation zeal: a misspelled kind would
// save without a word, display like the others, and NEVER be tried by the probe —
// exactly the defect this batch fixes for kind "http".
func checkKind(kind string) error {
	if credKinds[kind] {
		return nil
	}
	return fmt.Errorf("unknown credential kind: %q. Expected: ssh, telnet, http, or any (all transports)", kind)
}

func (s *Server) handleCreateKnownCredential(w http.ResponseWriter, r *http.Request) {
	var in knownCredInput
	if !decode(w, r, &in) {
		return
	}
	in.normalize()
	if in.Name == "" || in.Username == "" {
		respondErr(w, http.StatusBadRequest, "name and username required")
		return
	}
	var kindErr = checkKind(in.Kind)
	if kindErr != nil {
		respondErr(w, http.StatusBadRequest, kindErr.Error())
		return
	}
	var enc, err = s.Secrets.Seal(in.Secret)
	if err != nil {
		fail(w, err)
		return
	}
	var c model.KnownCredential
	c, err = s.Store.CreateKnownCredential(r.Context(), in.Name, in.Kind, in.Username,
		enc, in.Priority, in.Enabled)
	if err != nil {
		fail(w, err)
		return
	}
	s.emit(r, model.Event{Type: "credential.created", Severity: model.SevNotice,
		Message: fmt.Sprintf("standard credential \"%s\" (%s@) added", c.Name, c.Username)})
	respond(w, http.StatusCreated, c)
}

func (s *Server) handleUpdateKnownCredential(w http.ResponseWriter, r *http.Request) {
	var in knownCredInput
	if !decode(w, r, &in) {
		return
	}
	in.normalize()
	var kindErr = checkKind(in.Kind)
	if kindErr != nil {
		respondErr(w, http.StatusBadRequest, kindErr.Error())
		return
	}
	// Empty secret = unchanged: the UI never re-reads the secret, so it cannot send it back as-is.
	var enc []byte
	if in.Secret != "" {
		var err error
		enc, err = s.Secrets.Seal(in.Secret)
		if err != nil {
			fail(w, err)
			return
		}
	}
	var c, err = s.Store.UpdateKnownCredential(r.Context(), chi.URLParam(r, "id"),
		in.Name, in.Kind, in.Username, enc, in.Priority, in.Enabled)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, c)
}

func (s *Server) handleDeleteKnownCredential(w http.ResponseWriter, r *http.Request) {
	var err = s.Store.DeleteKnownCredential(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusNoContent, nil)
}

// --- Proxies usable for discovery ---

func (s *Server) handleListDiscoveryProxies(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var proxies, err = s.Store.ListDiscoveryProxies(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, proxies)
}

// dialerFor chooses where connections originate: the controller itself for the
// "Local" proxy, the proxy tunnel otherwise. A remote proxy without an established
// tunnel is ordinary operations (proxy down, WAN cut): say so clearly rather than an obscure timeout.
func (s *Server) dialerFor(proxyID string) (discovery.Dialer, error) {
	if proxyID == "" || proxyID == store.LocalProxyID {
		return nil, nil
	}
	if !s.Tunnels.Connected(proxyID) {
		return nil, errNoTunnel
	}
	return &proxyDialer{hub: s.Tunnels, proxyID: proxyID}, nil
}

var errNoTunnel = errors.New("this proxy has no tunnel established with the controller: " +
	"check that it is running and reaches the controller over outbound HTTPS")

// proxyDialer adapts the hub to the interface expected by the probe.
type proxyDialer struct {
	hub     *tunnel.Hub
	proxyID string
}

func (d *proxyDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("transport %s not tunnelable", network)
	}
	var host, portStr, err = net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var port int
	port, err = strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid port %q", portStr)
	}
	return d.hub.Dial(ctx, d.proxyID, host, port)
}

// handleDeviceFetch reads a page from a device's web interface, through the chosen
// proxy. READ-ONLY (GET/HEAD): the tool to understand an unknown interface — and write
// a web-only connector — without modifying the device.
//
// Reserved to global-admin: the primitive emits an HTTP request from inside a client's
// network. The proxy already accepts arbitrary dials from the controller by design
// (docs/03-Security/Proxy security), and its safeguard stays local: MESHCRAFT_DIAL_ALLOW.
func (s *Server) handleDeviceFetch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProxyID string `json:"proxy_id"`
		URL     string `json:"url"`
		Method  string `json:"method"`
		// Username / Password / Auth: credentials presented IF the device returns 401.
		//
		// Without them, this tool shows only what an interface serves to an anonymous
		// caller — i.e. on lines that authenticate at HTTP level, an error page. SwOS
		// returns 401 on all data pages and RouterOS REST on all resources: reading
		// without credentials looked like a mute device, when it simply waited to be
		// introduced.
		//
		// Empty `auth` lets the device choose the scheme in its challenge — preferred,
		// cf. driver.AuthAuto.
		Username string `json:"username"`
		Password string `json:"password"`
		Auth     string `json:"auth"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Method == "" {
		in.Method = http.MethodGet
	}
	if in.Method != http.MethodGet && in.Method != http.MethodHead {
		respondErr(w, http.StatusBadRequest,
			"read-only: only GET and HEAD are accepted here. Writing to a device goes through a connector, not this tool.")
		return
	}
	if !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
		respondErr(w, http.StatusBadRequest, "absolute url expected (http:// or https://)")
		return
	}
	var dialer, dialErr = s.dialerFor(in.ProxyID)
	if dialErr != nil {
		respondErr(w, http.StatusServiceUnavailable, dialErr.Error())
		return
	}
	var ctx, cancel = context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	var session = driver.NewWebSession("", dialer, 20*time.Second)
	if in.Username != "" {
		var mode = driver.AuthMode(strings.ToLower(strings.TrimSpace(in.Auth)))
		if mode == driver.AuthNone {
			mode = driver.AuthAuto
		}
		session.SetAuth(mode, in.Username, in.Password)
	}
	var resp, err = session.Get(ctx, in.URL)
	if err != nil {
		respondErr(w, http.StatusBadGateway, firstLine(err.Error()))
		return
	}
	s.emitInstance(r, "device.page_read", model.SevInfo,
		"Read "+in.URL+" via proxy")
	respond(w, http.StatusOK, resp)
}

func firstLine(s string) string {
	var i = strings.IndexAny(s, "\r\n")
	if i >= 0 {
		return s[:i]
	}
	return s
}

// handleOnboardDevice runs onboarding for a device not yet adopted — for Instant On,
// "Configure Locally" moving it from cloud portal wait to local administration.
//
// This WRITES to the device, a durable vendor-side choice: the connector checks state
// before and re-reads after. Rights are those of the proxy used — same gesture as
// declaring a scan on it.
//
// REFUSAL IS MISSING CAPABILITY, NOT STRING COMPARISON. This handler tested
// `in.Driver != "aruba-instant-on-1930"`, putting any future platform on the same
// side as those that need nothing — without saying so. Now [driver.Onboarder]: neither
// SwOS nor RouterOS implement it, because they are manageable out of the box, and their
// refusal explains that instead of naming a product line.
//
// THE ENTIRE SEQUENCE MOVED INTO THE CONNECTOR, identically — portal state, identity
// best-effort, then `PUT /managementPersonality` — and that is the point: `Changed`,
// BEFORE/AFTER comparison, is the ONLY proof on this API, where 2xx that changes
// nothing is silent failure. Split between handler and connector, it stopped happening
// where the write happens.
func (s *Server) handleOnboardDevice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProxyID string `json:"proxy_id"`
		IP      string `json:"ip"`
		Driver  string `json:"driver"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ProxyID == "" {
		in.ProxyID = store.LocalProxyID
	}
	var allowed, err = s.canAdminProxy(r, in.ProxyID)
	if err != nil {
		fail(w, err)
		return
	}
	if !allowed {
		respondErr(w, http.StatusForbidden, "insufficient rights on this proxy")
		return
	}
	if strings.TrimSpace(in.IP) == "" {
		respondErr(w, http.StatusBadRequest, "ip required")
		return
	}
	var conn, connErr = driver.ConnectorFor(in.Driver)
	if connErr != nil {
		respondErr(w, http.StatusUnprocessableEntity, connErr.Error())
		return
	}
	var onboarder, canOnboard = conn.(driver.Onboarder)
	if !canOnboard {
		respondErr(w, http.StatusUnprocessableEntity, driver.NotSupported(
			conn.Info().Name, "onboard this device",
			"this platform imposes no management mode choice: it is manageable out of the box, "+
				"so there is nothing to switch. Adopt it directly").Error())
		return
	}
	var dialer, dialErr = s.dialerFor(in.ProxyID)
	if dialErr != nil {
		respondErr(w, http.StatusServiceUnavailable, dialErr.Error())
		return
	}
	var ctx, cancel = context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	var res, onboardErr = onboarder.Onboard(ctx, driver.Target{Host: in.IP, Dialer: dialer})
	if onboardErr != nil {
		s.emitInstance(r, "device.onboard_failed", model.SevWarning,
			"Onboarding of "+in.IP+" failed: "+firstLine(onboardErr.Error()))
		// Transcript accompanies failure: on an API whose write gesture was reconstructed
		// by reading the device bundle, it is the only way to understand a refusal without
		// returning on site.
		respond(w, http.StatusBadGateway, map[string]any{
			"error": firstLine(onboardErr.Error()), "result": res,
		})
		return
	}
	s.emitInstance(r, "device.onboarded", model.SevNotice,
		"Device "+in.IP+" ("+res.Identity.Model+", serial "+res.Identity.Serial+
			") switched to local administration — "+stageChangeLabel(res))
	respond(w, http.StatusOK, res)
}

// stageChangeLabel says whether something happened, in words that do not look like
// success when nothing moved.
//
// `Changed` is the only proof the gesture worked: 2xx followed by identical state is
// FAILURE, and must not read as quiet success in the instance log.
func stageChangeLabel(res driver.OnboardResult) string {
	if res.Changed {
		return "stage \"" + res.Was + "\" -> \"" + res.Stage + "\""
	}
	return "UNCHANGED state (\"" + res.Stage + "\"): the device accepted the request " +
		"but did nothing"
}

// handleReachCheck: reachability preflight from a proxy. Read-only.
//
// Used before any management addressing switchover: sending a device into a subnet
// the proxy cannot reach loses it (docs/01-Architecture/Management addressing section 5).
func (s *Server) handleReachCheck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProxyID string `json:"proxy_id"`
		Host    string `json:"host"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ProxyID == "" {
		in.ProxyID = store.LocalProxyID
	}
	var allowed, err = s.canAdminProxy(r, in.ProxyID)
	if err != nil {
		fail(w, err)
		return
	}
	if !allowed {
		respondErr(w, http.StatusForbidden, "insufficient rights on this proxy")
		return
	}
	if strings.TrimSpace(in.Host) == "" {
		respondErr(w, http.StatusBadRequest, "host required")
		return
	}
	var dialer, dialErr = s.dialerFor(in.ProxyID)
	if dialErr != nil {
		respondErr(w, http.StatusServiceUnavailable, dialErr.Error())
		return
	}
	var ctx, cancel = context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	respond(w, http.StatusOK, driver.CheckReach(ctx, in.Host, dialer, 3*time.Second))
}

// --- Address domains ---
//
// A domain says WHICH NETWORK we mean, where a proxy only says HOW we get there
// (cf. internal/netscope and migration 0020).
//
// Rights: read for any authenticated user, write reserved to global-admin. Same
// reasoning as migration 0007. Attaching two proxies to one domain declares their
// networks are one — therefore merging their candidates. A site-admin who could do
// this alone would steer observations into a neighbor site's identity space, which is
// escalation: they do not administer both sides of the decision.

func (s *Server) handleListAddressDomains(w http.ResponseWriter, r *http.Request) {
	var domains, err = s.Store.ListAddressDomains(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, domains)
}

type addressDomainInput struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

func (s *Server) handleCreateAddressDomain(w http.ResponseWriter, r *http.Request) {
	var in addressDomainInput
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		respondErr(w, http.StatusBadRequest,
			"name this domain: \"head office network\", \"shared server VLAN\"… A nameless domain will not be chosen correctly by anyone.")
		return
	}
	var d, err = s.Store.CreateAddressDomain(r.Context(), in.Name, in.Note)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitInstance(r, "netscope.domain_created", model.SevNotice,
		"Address domain \""+d.Name+"\" created")
	respond(w, http.StatusCreated, d)
}

func (s *Server) handleUpdateAddressDomain(w http.ResponseWriter, r *http.Request) {
	var in addressDomainInput
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		respondErr(w, http.StatusBadRequest, "name required")
		return
	}
	var d, err = s.Store.UpdateAddressDomain(r.Context(), chi.URLParam(r, "id"), in.Name, in.Note)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, d)
}

func (s *Server) handleDeleteAddressDomain(w http.ResponseWriter, r *http.Request) {
	var err = s.Store.DeleteAddressDomain(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrDomainInUse) {
		respondErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusNoContent, nil)
}

// --- Scan ranges ---

func (s *Server) handleListScanPrefixes(w http.ResponseWriter, r *http.Request) {
	var prefixes, err = s.Store.ListScanPrefixes(r.Context(), r.URL.Query().Get("proxy"))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, prefixes)
}

type scanPrefixInput struct {
	ProxyID string `json:"proxy_id"`
	Prefix  string `json:"prefix"`
	Note    string `json:"note"`
	Enabled bool   `json:"enabled"`
	// AddressDomainID: the network this range describes. Empty = the proxy's own
	// domain, i.e. "a network of its own". That default is the only safe one: assuming
	// two proxies see the same network would merge distinct devices into one record,
	// which cannot be undone — whereas a duplicate deletes in one click.
	AddressDomainID string `json:"address_domain_id"`
}

// checkDomainChoice refuses attaching a range to a domain of a proxy the administrator
// does not administer.
//
// Without this safeguard, site A's administrator would declare their range in site B's
// domain: their candidates would merge with B's, and they could take over a record that
// is not theirs. Same escalation as migration 0007, by another path.
//
// A domain nobody uses yet is free: it reveals nothing.
func (s *Server) checkDomainChoice(r *http.Request, domainID string) error {
	if domainID == "" || auth.UserFrom(r.Context()).IsGlobalAdmin {
		return nil
	}
	var users, err = s.Store.ProxiesUsingDomain(r.Context(), domainID)
	if err != nil {
		return err
	}
	var p model.Proxy
	for _, p = range users {
		if !s.canAdminWholeProxy(r, p) {
			return fmt.Errorf(
				"address domain is already that of proxy %q, which you do not administer: "+
					"attaching your range there would merge your discoveries with theirs. Keep the "+
					"default domain, or ask an instance administrator to reconcile.",
				p.Name)
		}
	}
	return nil
}

func (s *Server) handleCreateScanPrefix(w http.ResponseWriter, r *http.Request) {
	var in scanPrefixInput
	if !decode(w, r, &in) {
		return
	}
	if in.ProxyID == "" {
		in.ProxyID = store.LocalProxyID
	}
	var allowed, err = s.canAdminProxy(r, in.ProxyID)
	if err != nil {
		fail(w, err)
		return
	}
	if !allowed {
		respondErr(w, http.StatusForbidden, "insufficient rights on this proxy")
		return
	}
	var norm string
	norm, err = discovery.NormalizePrefix(strings.TrimSpace(in.Prefix))
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err = s.checkDomainChoice(r, in.AddressDomainID); err != nil {
		respondErr(w, http.StatusForbidden, err.Error())
		return
	}
	// Input controls only FORMAT: a /16 or /64 is a site addressing fact, it must be
	// declarable. Scanning will say whether it can traverse it, and why not.
	var sp model.ScanPrefix
	sp, err = s.Store.CreateScanPrefix(r.Context(), in.ProxyID, norm, in.Note,
		in.AddressDomainID, in.Enabled)
	if err != nil {
		fail(w, err)
		return
	}
	// Range goes to the proxy immediately: useless until it knows it, and waiting for
	// the next tunnel reconnect would look like a declaration with no effect.
	go s.PushWatchTo(context.Background(), sp.ProxyID)
	respond(w, http.StatusCreated, sp)
}

func (s *Server) handleUpdateScanPrefix(w http.ResponseWriter, r *http.Request) {
	var sp, err = s.Store.GetScanPrefix(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	var allowed bool
	allowed, err = s.canAdminProxy(r, sp.ProxyID)
	if err != nil {
		fail(w, err)
		return
	}
	if !allowed {
		respondErr(w, http.StatusForbidden, "insufficient rights on this proxy")
		return
	}
	var in scanPrefixInput
	if !decode(w, r, &in) {
		return
	}
	var norm string
	norm, err = discovery.NormalizePrefix(strings.TrimSpace(in.Prefix))
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err = s.checkDomainChoice(r, in.AddressDomainID); err != nil {
		respondErr(w, http.StatusForbidden, err.Error())
		return
	}
	var updated model.ScanPrefix
	updated, err = s.Store.UpdateScanPrefix(r.Context(), sp.ID, norm, in.Note,
		in.AddressDomainID, in.Enabled)
	if err != nil {
		fail(w, err)
		return
	}
	go s.PushWatchTo(context.Background(), updated.ProxyID)
	respond(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteScanPrefix(w http.ResponseWriter, r *http.Request) {
	var sp, err = s.Store.GetScanPrefix(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	var allowed bool
	allowed, err = s.canAdminProxy(r, sp.ProxyID)
	if err != nil {
		fail(w, err)
		return
	}
	if !allowed {
		respondErr(w, http.StatusForbidden, "insufficient rights on this proxy")
		return
	}
	if err = s.Store.DeleteScanPrefix(r.Context(), sp.ID); err != nil {
		fail(w, err)
		return
	}
	// Proxy must STOP scanning: without this push, it would continue until next reconnect.
	go s.PushWatchTo(context.Background(), sp.ProxyID)
	respond(w, http.StatusNoContent, nil)
}

// scanTimeout bounds a scan: beyond it, return what was found — SAYING SO (cf.
// ScanReport.Truncated). Budget follows range size: a /24 finishes in seconds, a /16
// needs minutes.
const scanTimeout = 4 * time.Minute

func scanBudget(addresses int) time.Duration {
	if addresses > 8192 {
		return 12 * time.Minute
	}
	return scanTimeout
}

// handleRunScan scans a range NOW and returns the report.
//
// Assumed limit of this phase: only the "Local" proxy can run a scan, because the WSS
// tunnel to remote proxies does not exist yet (roadmap phase 0bis). A range declared
// on a remote proxy is accepted and kept, but its scan returns 501 rather than faking
// a scan that did not happen.
func (s *Server) handleRunScan(w http.ResponseWriter, r *http.Request) {
	var sp, err = s.Store.GetScanPrefix(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	var allowed bool
	allowed, err = s.canAdminProxy(r, sp.ProxyID)
	if err != nil {
		fail(w, err)
		return
	}
	if !allowed {
		respondErr(w, http.StatusForbidden, "insufficient rights on this proxy")
		return
	}
	var dialer, dialErr = s.dialerFor(sp.ProxyID)
	if dialErr != nil {
		respondErr(w, http.StatusServiceUnavailable, dialErr.Error())
		return
	}

	// HERE we decide whether the range is traversable, with reason.
	var enumErr = discovery.Enumerable(sp.Prefix)
	if enumErr != nil {
		respondErr(w, http.StatusUnprocessableEntity, enumErr.Error())
		return
	}

	var creds []discovery.Credential
	creds, err = s.discoveryCredentials(r.Context())
	if err != nil {
		fail(w, err)
		return
	}

	var addrs, expandErr = discovery.ExpandPrefix(sp.Prefix)
	if expandErr != nil {
		respondErr(w, http.StatusUnprocessableEntity, expandErr.Error())
		return
	}
	var ctx, cancel = context.WithTimeout(r.Context(), scanBudget(len(addrs)))
	defer cancel()

	var report discovery.ScanReport
	// Concurrency 0: the function adapts to range size.
	report, err = discovery.Scan(ctx, sp.Prefix, creds, 0, dialer)
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}

	var stored int
	var res discovery.Result
	for _, res = range report.Results {
		// Domain comes from the RANGE, not resolution: we scan exactly that network,
		// nothing to deduce.
		var _, upErr = s.storeCandidate(r.Context(), res, sp.ProxyID, sp.AddressDomainID, "scan")
		if upErr == nil {
			stored++
		}
	}
	var summary = fmt.Sprintf("%d addresses probed, %d devices kept (%s)",
		report.Addresses, stored, report.Duration)
	_ = s.Store.TouchScanPrefix(r.Context(), sp.ID, summary)
	s.emit(r, model.Event{Type: "discovery.scanned", Severity: model.SevNotice,
		Message: fmt.Sprintf("scan of %s: %s", sp.Prefix, summary)})

	respond(w, http.StatusOK, map[string]any{
		"prefix": sp.Prefix, "addresses": report.Addresses,
		"found": report.Found, "stored": stored, "duration": report.Duration,
	})
}

// discoveryCredentials decrypts active standard credentials. Secrets never leave
// this path: not log, not HTTP response.
func (s *Server) discoveryCredentials(ctx context.Context) ([]discovery.Credential, error) {
	var enc, err = s.Store.EnabledCredentials(ctx)
	if err != nil {
		return nil, err
	}
	var out = []discovery.Credential{}
	var e store.EncCredential
	for _, e = range enc {
		var secret string
		secret, err = s.Secrets.Open(e.SecretEnc)
		if err != nil {
			// A changed key makes a credential unusable: ignore it without breaking all
			// discovery. The gap is not lost — the admin screen signals it line by line
			// (cf. unreadableCredentials); here, silence is correct, but silence EVERYWHERE
			// would be invisible failure.
			continue
		}
		out = append(out, discovery.Credential{
			ID: e.ID, Name: e.Name, Kind: e.Kind, Username: e.Username, Secret: secret,
		})
	}
	return out, nil
}

// onlyCredential reduces the list to the chosen credential. Kind is KEPT: choosing a
// credential stored as "ssh" does not make it tryable on web — that is the operator's
// declaration, not ours to broaden.
func onlyCredential(creds []discovery.Credential, id string) []discovery.Credential {
	var out = []discovery.Credential{}
	var c discovery.Credential
	for _, c = range creds {
		if c.ID == id {
			out = append(out, c)
		}
	}
	return out
}

// storeCandidate turns a probe result into a candidate.
//
// `domainID` is the network in which the observed address makes sense. Not guessed:
// the caller holds it from the scanned range or store resolution. It decides whether
// two observations with the same IP are one device or two.
func (s *Server) storeCandidate(ctx context.Context, res discovery.Result, proxyID, domainID, source string) (model.Discovered, error) {
	var fingerprint, _ = json.Marshal(map[string]any{
		"open_ports":  res.OpenPorts,
		"ssh_banner":  res.SSHBanner,
		"http_server": res.HTTPServer,
		"http_title":  res.HTTPTitle,
		"evidence":    res.Evidence,
		// Faceplate read by the connector: gives adoption a drawable device without guessing catalog.
		"port_groups": res.PortGroups,
		"login_ok":    res.LoginOK,
		"credential":  res.CredentialName,
		// From web try: "refused" and "does not authenticate at HTTP level" need different
		// gestures, and the gap must survive scanning — the candidate queue is where we
		// re-read why a switch stayed anonymous. Only the CODE travels here, never the
		// credential presented.
		"web_auth": res.WebAuth,
	})
	var credID *string
	if res.CredentialID != "" {
		credID = &res.CredentialID
	}
	var d = model.Discovered{
		Source: source, ProxyID: proxyID, AddressDomainID: domainID,
		IP: res.IP, Serial: res.Serial, MAC: res.MAC,
		Hostname: res.Hostname, Vendor: res.Vendor, Model: res.Model,
		Driver: res.Driver, Confidence: res.Confidence,
		Transports:  strings.Join(res.Transports, ","),
		Fingerprint: fingerprint, CredentialID: credID, Note: res.Notes,
	}
	return s.Store.UpsertDiscovered(ctx, d, res.RawConfig)
}

// --- Candidate queue ---

// handleListDiscovered: adoption queue. `?adopted=1` adds already consumed candidates —
// an adopted device leaves the queue, and without this reminder the operator wonders
// where their switch went.
func (s *Server) handleListDiscovered(w http.ResponseWriter, r *http.Request) {
	var list, err = s.Store.ListDiscovered(r.Context(),
		r.URL.Query().Get("adopted") == "1")
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, list)
}

func (s *Server) handleGetDiscoveredConfig(w http.ResponseWriter, r *http.Request) {
	var raw, err = s.Store.GetDiscoveredConfig(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	var parsed any
	var parseErr string
	var res, pErr = confimport.Parse(raw, "")
	if pErr != nil {
		parseErr = pErr.Error()
	} else {
		parsed = res
	}
	respond(w, http.StatusOK, map[string]any{
		"raw_config": raw, "parsed": parsed, "parse_error": parseErr,
	})
}

func (s *Server) handleDeleteDiscovered(w http.ResponseWriter, r *http.Request) {
	var err = s.Store.DeleteDiscovered(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusNoContent, nil)
}

// --- Manual add by IP ---

type probeInput struct {
	IP      string `json:"ip"`
	ProxyID string `json:"proxy_id"`
	// One-off optional credentials: otherwise try instance standard credentials.
	Username string `json:"username"`
	Password string `json:"password"`
	// CredentialID: try THIS stored credential, and it alone.
	//
	// Without this field, choice was "all instance credentials" or "I retype the
	// password" — retyping a password already entrusted to the instance is exactly what
	// we no longer want to ask. Also useful to verify a stored credential really works on
	// a device, without letting the whole list run and consume the two-failure budget.
	CredentialID string `json:"credential_id"`
	// Driver: user-imposed type; empty = auto-detect.
	Driver string `json:"driver"`
	// FetchConfig: also read current configuration for import at adoption.
	FetchConfig bool `json:"fetch_config"`
}

const probeTimeout = 45 * time.Second

func (s *Server) handleProbeIP(w http.ResponseWriter, r *http.Request) {
	var in probeInput
	if !decode(w, r, &in) {
		return
	}
	in.IP = strings.TrimSpace(in.IP)
	if in.ProxyID == "" {
		in.ProxyID = store.LocalProxyID
	}
	var allowed, err = s.canAdminProxy(r, in.ProxyID)
	if err != nil {
		fail(w, err)
		return
	}
	if !allowed {
		respondErr(w, http.StatusForbidden, "insufficient rights on this proxy")
		return
	}
	var addrs []string
	addrs, err = discovery.ExpandPrefix(in.IP)
	if err != nil || len(addrs) != 1 {
		respondErr(w, http.StatusBadRequest, "single IP address expected (v4 or v6)")
		return
	}
	var dialer, dialErr = s.dialerFor(in.ProxyID)
	if dialErr != nil {
		respondErr(w, http.StatusServiceUnavailable, dialErr.Error())
		return
	}

	var creds []discovery.Credential
	switch {
	case in.Username != "":
		// Credential supplied for this try only: not stored.
		//
		// EMPTY kind is the fix: forced to "ssh", this pair was never presented to the web
		// interface — never tried at all on a web-only line. Whoever enters a password for
		// "this switch" should not guess which transport meshcraft will use.
		creds = []discovery.Credential{{
			ID: "", Name: "saisi manuellement", Kind: "",
			Username: in.Username, Secret: in.Password,
		}}
	case in.CredentialID != "":
		creds, err = s.discoveryCredentials(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		creds = onlyCredential(creds, in.CredentialID)
		if len(creds) == 0 {
			// Chosen then missing: deleted in the meantime, or secret no longer decrypts.
			// Either way, probing "with all others" would do something other than requested.
			respondErr(w, http.StatusUnprocessableEntity,
				"this stored credential is missing or unusable "+
					"(deleted, disabled, or secret unreadable with the current key)")
			return
		}
	default:
		creds, err = s.discoveryCredentials(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
	}

	var ctx, cancel = context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()
	var prober = &discovery.Prober{
		FetchConfig: in.FetchConfig, ForceDriver: in.Driver, Dialer: dialer,
	}
	var res = prober.Probe(ctx, addrs[0], creds)

	if !res.Reachable {
		respondErr(w, http.StatusNotFound,
			fmt.Sprintf("no administration port open on %s (tried: %v)",
				addrs[0], discovery.ProbePorts))
		return
	}
	// One-off probe: address may fall outside any declared range. Store then resolves
	// to the proxy's own domain, better than leaving the observation without a network —
	// it would collide with all others sharing its address.
	var domainID string
	domainID, err = s.Store.DomainForAddress(r.Context(), in.ProxyID, res.IP)
	if err != nil {
		fail(w, err)
		return
	}
	var cand model.Discovered
	cand, err = s.storeCandidate(r.Context(), res, in.ProxyID, domainID, "manual")
	if err != nil {
		fail(w, err)
		return
	}
	s.emit(r, model.Event{Type: "discovery.probed", Severity: model.SevNotice,
		Message: fmt.Sprintf("manual probe of %s: %s (%s)", res.IP,
			orDash(res.Driver), res.Evidence)})
	respond(w, http.StatusOK, map[string]any{"candidate": cand, "probe": res})
}

// --- Add via serial console (Web Serial in browser) ---

type serialInput struct {
	// Banner: what the device wrote on the serial port (boot banner, prompt, ID command output).
	Banner string `json:"banner"`
	// RawConfig: configuration optionally captured on the console.
	RawConfig string `json:"raw_config"`
	IP        string `json:"ip"`
	Driver    string `json:"driver"`
	Note      string `json:"note"`
}

// handleSerialCandidate creates a candidate from a serial session.
//
// The browser talks to the serial port (Web Serial API); the controller only sees
// captured text. Enough to identify the device and import configuration — the use case:
// new or reset device, no IP reachable yet.
func (s *Server) handleSerialCandidate(w http.ResponseWriter, r *http.Request) {
	var in serialInput
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Banner) == "" && strings.TrimSpace(in.RawConfig) == "" {
		respondErr(w, http.StatusBadRequest,
			"nothing to analyze: capture at least the banner or configuration")
		return
	}
	var text = in.Banner + "\n" + in.RawConfig
	var m, conf, evidence = discovery.Identify(text, "", "", "")
	if in.Driver != "" {
		m = discovery.MatcherFor(in.Driver)
		evidence = "user-imposed type"
		conf = discovery.ConfShellOutput
	}
	var hostname, serial, mdl = discovery.ExtractFacts(text)
	var fingerprint, _ = json.Marshal(map[string]any{
		"evidence": evidence, "source": "serial console",
		"capture_len": len(text),
	})

	// Serial console goes through the browser: the device was not reached by any proxy,
	// and the entered address is only a hint. Attach to the "Local" proxy network for lack
	// of better — mainly so it is not domainless, which would collide with any other
	// observation of the same address.
	var domainID, domErr = s.Store.DomainForAddress(r.Context(), store.LocalProxyID,
		strings.TrimSpace(in.IP))
	if domErr != nil {
		fail(w, domErr)
		return
	}
	var d = model.Discovered{
		Source: "serial", ProxyID: store.LocalProxyID, AddressDomainID: domainID,
		IP:     strings.TrimSpace(in.IP),
		Serial: serial, Hostname: hostname, Vendor: m.Vendor, Model: mdl,
		Driver: m.Driver, Confidence: conf, Transports: "serial",
		Fingerprint: fingerprint, Note: in.Note,
	}
	var cand, err = s.Store.UpsertDiscovered(r.Context(), d, in.RawConfig)
	if err != nil {
		fail(w, err)
		return
	}
	s.emit(r, model.Event{Type: "discovery.serial", Severity: model.SevNotice,
		Message: fmt.Sprintf("candidate added via serial console: %s (%s)",
			orDash(hostname), orDash(m.Driver))})
	respond(w, http.StatusCreated, cand)
}

// --- Parse pasted configuration (preview before adoption) ---

type parseInput struct {
	RawConfig string `json:"raw_config"`
	Dialect   string `json:"dialect"`
}

func (s *Server) handleParseConfig(w http.ResponseWriter, r *http.Request) {
	var in parseInput
	if !decode(w, r, &in) {
		return
	}
	var res, err = confimport.Parse(in.RawConfig, in.Dialect)
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	respond(w, http.StatusOK, res)
}

// --- Adoption ---

type adoptInput struct {
	SiteID   string `json:"site_id"`
	Name     string `json:"name"`
	Location string `json:"location"`
	// ConfigMode: "push" = platform is source of truth (device starts blank in model);
	// "import" = import read configuration.
	ConfigMode string `json:"config_mode"`
	// RawConfig: configuration supplied at adoption (paste from serial console). Otherwise
	// what is already stored on the candidate.
	RawConfig string `json:"raw_config"`
}

func (s *Server) handleAdoptDiscovered(w http.ResponseWriter, r *http.Request) {
	var in adoptInput
	if !decode(w, r, &in) {
		return
	}
	// HERE the site is chosen, and rights are checked on THAT site.
	if !s.Auth.CanSite(r.Context(), in.SiteID, "operator") {
		respondErr(w, http.StatusForbidden,
			"insufficient rights on the chosen site (role \"operator\" required to adopt)")
		return
	}
	var cand, err = s.Store.GetDiscovered(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	if cand.AdoptedDevice != nil {
		respondErr(w, http.StatusConflict, "this candidate was already adopted")
		return
	}

	var name = strings.TrimSpace(in.Name)
	if name == "" {
		name = cand.Hostname
	}
	if name == "" {
		name = cand.IP
	}
	if name == "" {
		respondErr(w, http.StatusBadRequest, "name required")
		return
	}

	var vlans = []model.Vlan{}
	var ports = json.RawMessage(`{}`)
	var portGroups = json.RawMessage(`[]`)
	var warnings = []string{}

	// "push" mode: device starts blank in the model, BUT physical faceplate is fact not
	// intent. If the connector read it at discovery, reuse it — otherwise adoption would
	// have no ports.
	if in.ConfigMode != "import" {
		var fp map[string]any
		if json.Unmarshal(cand.Fingerprint, &fp) == nil {
			var pg, ok = fp["port_groups"]
			if ok && pg != nil {
				var raw, mErr = json.Marshal(pg)
				if mErr == nil && string(raw) != "null" && string(raw) != "[]" {
					portGroups = raw
				}
			}
		}
	}

	if in.ConfigMode == "import" {
		var raw = in.RawConfig
		if strings.TrimSpace(raw) == "" {
			raw, err = s.Store.GetDiscoveredConfig(r.Context(), cand.ID)
			if err != nil {
				fail(w, err)
				return
			}
		}
		var parsed confimport.Result
		parsed, err = confimport.Parse(raw, "")
		if err != nil {
			respondErr(w, http.StatusBadRequest,
				"import failed: "+err.Error())
			return
		}
		ports, portGroups, vlans, warnings = fromParsed(parsed)
		if parsed.Hostname != "" && strings.TrimSpace(in.Name) == "" {
			name = parsed.Hostname
		}
	}

	var x, y float64
	x, y, err = s.Store.NextFreePosition(r.Context(), in.SiteID)
	if err != nil {
		fail(w, err)
		return
	}

	var dev = model.Device{
		SiteID: in.SiteID, Name: name, Model: cand.Model, Vendor: cand.Vendor,
		Location: strings.TrimSpace(in.Location), MgmtIP: cand.IP,
		Status: "unknown", PosX: x, PosY: y,
		PortGroups: portGroups, Ports: ports,
	}
	// Candidate driver and origin are kept: lets the connector take over again, and audit
	// know where the device came from.
	var extra, _ = json.Marshal(map[string]string{
		"driver": cand.Driver, "discovered_id": cand.ID,
		"config_mode": in.ConfigMode, "adopted_from": cand.Source,
	})
	dev.Extra = extra

	var created model.Device
	var vlansCreated, vlansKept []int
	created, vlansCreated, vlansKept, err = s.Store.AdoptDiscovered(r.Context(), cand.ID, dev, vlans)
	if err != nil {
		if err == store.ErrAlreadyAdopted {
			respondErr(w, http.StatusConflict, "this candidate was already adopted")
			return
		}
		fail(w, err)
		return
	}
	// COLLECTOR IS CHOSEN HERE, BEFORE SNMP PROVISIONING.
	//
	// Adoption left `proxy_id` empty, so the whole fleet lived under coverage-based
	// collection. Wrong default: without a named collector, meshcraft cannot ask BY WHICH
	// ADDRESS this device is reached, so it bounds the SNMP agent to the plan subnet only
	// — and a proxy reaching management by routing is not in it. The agent goes silent for
	// the only proxy meant to poll it, with no signal (cf. collectorForCandidate, which
	// documents this case).
	//
	// ORDER IS NOT NEGOTIABLE: `provisionSNMP` reads `d.ProxyID` to compose community source
	// bounds. Setting collector after would write a bound excluding its own collector, then
	// never fix it — `snmpConformant` would find conforming community and stop touching it.
	var collector, whyNoCollector = collectorForCandidate(cand,
		s.coveringProxies(r.Context(), in.SiteID))
	if collector.ID != "" {
		var setErr = s.Store.SetDeviceProxy(r.Context(), created.ID, collector.ID)
		if setErr != nil {
			warnings = append(warnings, "collector \""+collector.Name+"\" not saved ("+
				setErr.Error()+"): this device is polled by all proxies covering its site.")
		} else {
			// Copied into in-memory object: provisionSNMP receives IT, and re-reading the
			// record for three fields would cost one more query in a path that already does many.
			created.ProxyID, created.ProxyName = collector.ID, collector.Name
			// Watch is pushed to the collector: otherwise it polls this device only on next
			// reconnect. Background on fresh context, like handleSetDeviceCollector — request
			// context dies with the response.
			go s.PushWatchTo(context.Background(), collector.ID)
		}
	} else if whyNoCollector != "" {
		// SAID, never hidden. Coverage-based collection remains legitimate, but deprives this
		// device of trap destination and correct source bounds: the operator must attach one themselves.
		warnings = append(warnings, "no collector designated ("+whyNoCollector+
			"): this device will be polled by all proxies covering its site, will have no trap "+
			"recipient, and its SNMP agent source bounds cannot account for the address by "+
			"which it is reached.")
	}

	// ADOPTION is when device identity arrives: auto-tagging and SNMP identity are decided
	// then. This path forgot them — manually created devices got them, discovery-adopted
	// devices did not, exactly the opposite of what adoption should mean.
	//
	// Neither can fail adoption: an adopted but mis-filed device can be fixed; a refused
	// adoption stays out.
	s.autoTagDevice(r.Context(), created)
	s.provisionSNMP(r.Context(), created)

	// An existing VLAN was KEPT as-is: its name comes from the user, not the device. Say
	// so, or the screen implies the VLAN plan read on the switch was applied.
	if len(vlansKept) > 0 {
		var existing, listErr = s.Store.ListVlans(r.Context(), in.SiteID)
		var nameOf = map[int]string{}
		if listErr == nil {
			var ev model.Vlan
			for _, ev = range existing {
				nameOf[ev.VlanID] = ev.Name
			}
		}
		var vid int
		for _, vid = range vlansKept {
			var readName = ""
			var pv model.Vlan
			for _, pv = range vlans {
				if pv.VlanID == vid {
					readName = pv.Name
				}
			}
			warnings = append(warnings, fmt.Sprintf(
				"VLAN %d already existed on site as %q: kept, name read on device (%q) not applied",
				vid, orDash(nameOf[vid]), readName))
		}
	}
	// Extra is not set by adoption insert: apply it afterward.
	created.Extra = extra
	_, _ = s.Store.UpdateDevice(r.Context(), created)

	var mode = "configuration pushed by the platform"
	if in.ConfigMode == "import" {
		mode = fmt.Sprintf("configuration imported from device (%d ports, %d VLANs created, %d already present)",
			countPorts(ports), len(vlansCreated), len(vlansKept))
	}
	s.emitSite(r, in.SiteID, "device.adopted", model.SevNotice,
		fmt.Sprintf("%q adopted from discovery — %s", created.Name, mode),
		map[string]any{"driver": cand.Driver, "ip": cand.IP, "source": cand.Source})

	respond(w, http.StatusCreated, map[string]any{
		"device":        created,
		"vlans_created": len(vlansCreated),
		"vlans_kept":    len(vlansKept),
		"warnings":      warnings,
	})
}

// fromParsed maps parser output to device columns.
func fromParsed(p confimport.Result) (ports, portGroups json.RawMessage, vlans []model.Vlan, warnings []string) {
	var portsMap = map[string]any{}
	var name string
	var port confimport.Port
	for name, port = range p.Ports {
		var entry = map[string]any{"mode": port.Mode}
		if port.Label != "" {
			entry["label"] = port.Label
		}
		if port.PVID != nil {
			entry["pvid"] = *port.PVID
		}
		if port.Tagged != nil {
			entry["tagged"] = port.Tagged
		}
		portsMap[name] = entry
	}
	ports, _ = json.Marshal(portsMap)

	// Shape expected by the UI faceplate: prefix + port count, plus start index, because
	// not all vendors number from 1.
	var groups = []map[string]any{}
	var g confimport.PortGroup
	for _, g = range p.PortGroups {
		groups = append(groups, map[string]any{
			"prefix": g.Prefix,
			"from":   g.From,
			"count":  g.To - g.From + 1,
			"speed":  speedLabel(g.SpeedMbps),
			"poe":    g.PoE,
		})
	}
	portGroups, _ = json.Marshal(groups)

	vlans = []model.Vlan{}
	var v confimport.Vlan
	var i int
	for i, v = range p.Vlans {
		var vname = v.Name
		if vname == "" {
			vname = fmt.Sprintf("VLAN %d", v.VlanID)
		}
		vlans = append(vlans, model.Vlan{
			VlanID: v.VlanID, Name: vname,
			Color:   defaultVlanColor(i),
			Purpose: "imported from device",
		})
	}
	return ports, portGroups, vlans, p.Warnings
}

// defaultVlanColor reuses the UI palette, validated for color vision (cf. web/src/network.ts).
var vlanPalette = []string{
	"#3987e5", "#d95926", "#199e70", "#c98500", "#d55181",
	"#008300", "#9085e9", "#e66767",
}

func defaultVlanColor(i int) string {
	return vlanPalette[i%len(vlanPalette)]
}

// speedLabel maps speed to short label, like the catalog.
func speedLabel(mbps int) string {
	if mbps >= 1000 {
		return fmt.Sprintf("%dG", mbps/1000)
	}
	return fmt.Sprintf("%dM", mbps)
}

func countPorts(raw json.RawMessage) int {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return 0
	}
	return len(m)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// --- Catalog: drivers and reset procedures ---

func (s *Server) handleListDrivers(w http.ResponseWriter, r *http.Request) {
	respond(w, http.StatusOK, discovery.Drivers())
}

// handleResetProcedures: search by vendor, product line, or model.
func (s *Server) handleResetProcedures(w http.ResponseWriter, r *http.Request) {
	respond(w, http.StatusOK, discovery.SearchResetProcedures(r.URL.Query().Get("q")))
}

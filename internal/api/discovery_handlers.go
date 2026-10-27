package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
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
// a site (cf. docs/en/03-Security/RBAC.md).

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
// handleRevealKnownCredential puts ONE instance credential's secret on screen,
// and journals that it happened.
//
// # Why this exists, and what it repairs
//
// Site credentials have had an audited reveal from the start
// (cf. sitecreds_handlers.go). Instance credentials had none — and they are the
// ones actually sitting on any device provisioned over the serial console but not
// yet adopted, because the soft-reset wizard places THEM: the browser has to type
// a secret on a UART the controller cannot open, so it uses the instance
// credential, and the site credential is only pushed later by the adoption path.
//
// The practical consequence, observed on a Catalyst 2960-X and an FS S3900 whose
// adoption had not completed: the operator holds a device whose password exists in
// meshcraft, is displayed nowhere, and cannot be recovered except by re-running the
// serial wizard — the only path that ever returned it
// (cf. handleSerialCredential, which materializes it for typing). A vault that
// cannot show you the password on the device in front of you is not doing its job.
//
// # The same gesture as the site reveal, deliberately
//
// Same severity (warning), same shape of event, same "it is logged" wording on
// screen. Two reveal paths that journal differently would make the audit trail
// depend on which page someone happened to open, and the whole value of the trail
// is that it does not.
//
// Global-admin, inherited from the route group: instance credentials are not
// scoped to a site, so no site role can bound who reads them.
func (s *Server) handleRevealKnownCredential(w http.ResponseWriter, r *http.Request) {
	if s.Secrets == nil {
		respondErr(w, http.StatusServiceUnavailable, "secret key not configured")
		return
	}
	var c, err = s.Store.RevealKnownCredential(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	var secret string
	secret, err = s.Secrets.Open(c.SecretEnc)
	if err != nil {
		// An unreadable secret is not a server fault to hide behind a 500: the
		// instance key has changed since this credential was written, the admin
		// screen already flags the row (cf. unreadableCredentials), and saying so
		// here is what connects the two.
		respondErr(w, http.StatusUnprocessableEntity,
			"this credential no longer decrypts with the instance key in use: "+
				"it was written with another one, and its secret is unrecoverable")
		return
	}
	var me = auth.UserFrom(r.Context())
	s.emitInstance(r, "credential.revealed", model.SevWarning,
		"Instance credential \""+c.Name+"\" revealed by "+me.Email)
	respond(w, http.StatusOK, map[string]any{
		"id": c.ID, "name": c.Name, "kind": c.Kind,
		"username": c.Username, "secret": secret,
	})
}

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

// RouteProxyID names the proxy this dialer leaves from — what the external
// driver runtime freezes into a SOCKS credential, so a driver container's
// connections leave from EXACTLY the proxy the in-process path would use.
// (restdriver asserts this method; a nil Dialer reads as "from the
// controller machine", same as everywhere else.)
func (d *proxyDialer) RouteProxyID() string { return d.proxyID }

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
// (docs/en/03-Security/Proxy security.md), and its safeguard stays local: MESHCRAFT_DIAL_ALLOW.
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
// the proxy cannot reach loses it (docs/en/01-Architecture/Management addressing.md section 5).
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

// handleSerialCredential decrypts one instance credential for Web Serial
// soft-reset / adopt scripts. Secrets otherwise never leave the controller;
// this path exists because the browser holds the UART and must type the
// password. Prefer a specific credential_id; empty = highest-priority enabled.
func (s *Server) handleSerialCredential(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CredentialID string `json:"credential_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if s.Secrets == nil {
		respondErr(w, http.StatusServiceUnavailable, "secret key not configured")
		return
	}
	var creds, err = s.discoveryCredentials(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	if in.CredentialID != "" {
		creds = onlyCredential(creds, in.CredentialID)
	}
	if len(creds) == 0 {
		respondErr(w, http.StatusNotFound, "no usable discovery credential")
		return
	}
	var c = creds[0]
	respond(w, http.StatusOK, map[string]any{
		"id":       c.ID,
		"name":     c.Name,
		"username": c.Username,
		"secret":   c.Secret,
	})
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
			ID: "", Name: "manually entered", Kind: "",
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

	// THE PHYSICAL FACEPLATE IS FACT, NOT INTENT, and it is therefore taken from
	// what the connector saw at discovery WHATEVER the config mode is.
	//
	// This used to be `if in.ConfigMode != "import"`, on the reasonable ground
	// that an import derived its groups from the interface names in the config
	// text (fromParsed, below, which still overrides when it has any). That stops
	// being true the moment an import has no text to parse: the live path adopted
	// a CRS305 on 2026-08-12 with `port_groups: []`, so the device sheet had no
	// faceplate to draw at all — six imported port intents and nowhere to show
	// them. Importing a CONFIGURATION cannot change which jacks exist.
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

	// IMPORT WITH NO TEXT TO PARSE IS THE ORDINARY CASE, NOT AN ERROR.
	//
	// Discovery only captures a configuration when it can SCRAPE one over an
	// interactive SSH shell (discovery.Prober.ConfigCmd). That leaves out every
	// web-only range — and, measured on a CRS305 2026-08-12, RouterOS too: its
	// shell answers with terminal-control escape sequences, the expect-based
	// probe times out after 12 s, and identification falls back to the REST API,
	// which exports nothing. `has_config` is then false on a device meshcraft has
	// just identified with 96 % confidence and can log into.
	//
	// The probe already says so in its own words — "exporting a web-only range
	// belongs to its connector, not to a generic probe" — and this is that
	// connector path: when there is no text, the device is READ through its
	// connector after adoption and its real configuration absorbed. See
	// importFromDevice below.
	var importLive bool
	if in.ConfigMode == "import" {
		var raw = in.RawConfig
		if strings.TrimSpace(raw) == "" {
			raw, err = s.Store.GetDiscoveredConfig(r.Context(), cand.ID)
			if err != nil {
				fail(w, err)
				return
			}
		}
		importLive = strings.TrimSpace(raw) == ""
		if !importLive {
			var parsed confimport.Result
			parsed, err = confimport.Parse(raw, "")
			if err != nil {
				respondErr(w, http.StatusBadRequest,
					"import failed: "+err.Error())
				return
			}
			// On a NUMBERING connector (driver.PortNamer), a device adopted by
			// pasting its configuration must carry the SAME port vocabulary as one
			// adopted by push or read live over the connector — otherwise
			// devices.ports is keyed on the raw platform name here and on the
			// standard name there, and every later apply / diff / telemetry lookup
			// silently misses.
			var remapWarnings []string
			parsed.Ports, parsed.PortGroups, remapWarnings =
				remapPortsToStandardModel(cand.Driver, parsed.Ports, parsed.PortGroups)
			var parsedGroups json.RawMessage
			ports, parsedGroups, vlans, warnings = fromParsed(parsed)
			// Only when the text actually described a faceplate. A config whose
			// interface names this parser does not recognise yields an EMPTY group
			// list, and letting that through would erase the one the connector
			// read — same defect as the live path had, arriving by another door.
			if len(parsedGroups) > 0 && string(parsedGroups) != "[]" &&
				string(parsedGroups) != "null" {
				portGroups = parsedGroups
			}
			warnings = append(warnings, remapWarnings...)
			if parsed.Hostname != "" && strings.TrimSpace(in.Name) == "" {
				name = parsed.Hostname
			}
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
	var extraFields = map[string]string{
		"driver": cand.Driver, "discovered_id": cand.ID,
		"config_mode": in.ConfigMode, "adopted_from": cand.Source,
	}
	if strings.TrimSpace(cand.MAC) != "" {
		// The base MAC the connector read at inspection is CHASSIS IDENTITY: it is
		// what this device's neighbors will announce as `lldpRemChassisId`, and
		// keeping it lets the topology recognize the device even when its own SNMP
		// agent serves no LLDP-MIB (cf. deviceKnownMACs in lldp.go). Only written
		// when discovery actually read one — an empty key would assert nothing.
		extraFields["mac"] = strings.TrimSpace(cand.MAC)
	}
	var extra, _ = json.Marshal(extraFields)
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

	// THE LIVE IMPORT, once the device exists and can therefore be reached
	// through its connector with the site's stored credentials.
	//
	// AFTER creation and not before, which is the whole reason this is possible
	// at all: `adminFor` resolves credentials from a model.Device, and until
	// AdoptDiscovered returns there is no device to resolve them for. The cost is
	// that a failure here leaves an ADOPTED device carrying no imported
	// configuration — reported as a warning, never as a failed adoption, because
	// the device really was adopted and undoing that would be worse than saying
	// what did not happen.
	if importLive {
		var imported, liveWarn = s.importFromDevice(r.Context(), created)
		if liveWarn != "" {
			warnings = append(warnings, liveWarn)
		} else {
			if imported.declined != "" {
				warnings = append(warnings, imported.declined)
			}
			vlansCreated = append(vlansCreated, imported.vlans...)
			s.emitDeviceEvent(r, created, "config.absorbed", model.SevNotice,
				fmt.Sprintf("Configuration of %q read from the device and imported at "+
					"adoption (%d port(s), %d VLAN(s) created).",
					created.Name, imported.ports, len(imported.vlans)))
		}
	}

	respond(w, http.StatusCreated, map[string]any{
		"device":        created,
		"vlans_created": len(vlansCreated),
		"vlans_kept":    len(vlansKept),
		"warnings":      warnings,
	})
}

// liveImport is what a connector read produced at adoption.
type liveImport struct {
	ports int
	vlans []int
	// declined: the site VLANs this import deliberately did NOT create, and why.
	// Empty in the ordinary case. See absorbObserved for the rule and the
	// fleet-wide incident that produced it.
	declined string
}

// importFromDevice reads a freshly adopted device through its connector and
// absorbs what it finds: the site gains the VLANs the device already carries,
// and every port gains the intent the device is actually running.
//
// # Why this exists next to the text importer
//
// "Import the local configuration" used to mean ONE thing: parse a running-config
// somebody pasted, or that discovery scraped over an interactive SSH shell. That
// covers the IOS-like and Comware CLIs and nothing else — no web-only range, and
// not RouterOS, whose shell answers an expect-based probe with terminal-control
// escape sequences until it times out. On those platforms the option was offered
// and then greyed out, on a device meshcraft could identify to 96 % and log into.
//
// A connector read has no such limit and is strictly better where both apply: it
// returns the STRUCTURED state (driver.State — VLANs, per-port mode, PVID and
// tagged list) that the connector already knows how to obtain by whatever means
// its platform offers, instead of a text this package would have to learn a new
// dialect for. It is the same read the device page's own import button consumes,
// which is why both end in [Server.absorbObserved].
//
// # Failure is a SENTENCE, never an error
//
// Every return path yields a phrase for the operator rather than an error,
// because the caller has already adopted the device: a refusal here must not
// unwind that. The phrases name which half did not happen — the session, the
// read, or the absorb — since the fix differs (a credential, a reachable
// management address, a connector that cannot read state at all).
func (s *Server) importFromDevice(ctx context.Context, d model.Device) (liveImport, string) {
	var _, adm, _, err = s.adminFor(ctx, d)
	if adm != nil {
		defer adm.Close()
	}
	if err != nil {
		return liveImport{}, "the configuration could not be imported: no session could be " +
			"opened on the device (" + firstLine(err.Error()) + "). The device is adopted " +
			"and its configuration can be imported from its own page once it is reachable"
	}
	var state driver.State
	if state, err = adm.ReadState(ctx); err != nil {
		return liveImport{}, "the configuration could not be imported: the device refused " +
			"the read (" + firstLine(err.Error()) + "). The device is adopted; retry from " +
			"its own page"
	}
	if len(state.Ports) == 0 {
		// Absorbing an empty read would REPLACE the intent with nothing, which on
		// this path means adopting a switch and immediately declaring all of its
		// ports unconfigured.
		//
		// KEPT although absorbObserved now refuses this too, for two reasons it
		// alone can serve: it fires BEFORE the read is persisted, so the device
		// sheet does not open on an observation of nothing; and it names the
		// CONNECTOR as what came back empty, which is the fix — a credential or a
		// connector that cannot read state — rather than naming the model.
		return liveImport{}, "the configuration was not imported: the connector read no " +
			"port at all on this device, and absorbing an empty read would claim every " +
			"jack is unconfigured"
	}
	// Persisted BEFORE it is absorbed, so the device page shows the same
	// observation the import consumed — otherwise the sheet would open on "never
	// read back" beside a configuration that plainly came from somewhere.
	if err = s.saveObservedState(ctx, d.ID, state); err != nil {
		return liveImport{}, "the configuration was read but not saved: " + firstLine(err.Error())
	}
	var obs = observedState{VLANs: state.VLANs, Ports: state.Ports,
		LinkUp: state.LinkUp(), Links: state.Links, MgmtVLAN: state.MgmtVLAN}
	var ports, created, declined, aErr = s.absorbObserved(ctx, d, obs)
	// A SHORT READ ADOPTS THE DEVICE AND IMPORTS NOTHING (ADR-0017).
	//
	// This is the decision the incident of 2026-10-24 forced. sw01-arena was
	// adopted with 48 of its 52 ports — the four 10G uplinks absent, the 48 all
	// `access` on a seven-VLAN switch — and it read `in_sync`, because intent and
	// observation came out of this same truncated read. Nothing would ever have
	// flagged it, and an apply would have treated the uplinks as unconfigured.
	//
	// The arbitration, between two imperfect answers: refusing the whole adoption
	// keeps the device OUT, and a refused adoption is the one state the UI offers
	// no way out of — the candidate stays a candidate. Importing the 48 with a
	// warning keeps a model that is wrong AND claims to be right, and the warning
	// is read once. An EMPTY model is visibly empty: the faceplate is drawn, the
	// ports are free, and the operator finishes the gesture from the device page.
	// We keep the failure where it can still be seen.
	var short *shortRead
	if errors.As(aErr, &short) {
		return liveImport{}, "the configuration was NOT imported: " + short.Error() +
			". The device IS adopted and its port model left empty — read it again from " +
			"its own page and import from there, rather than carrying a configuration " +
			"that would claim to be in sync while missing ports"
	}
	if aErr != nil {
		return liveImport{}, "the configuration was read but not imported: " + firstLine(aErr.Error())
	}
	return liveImport{ports: ports, vlans: created, declined: declined}, ""
}

// remapPortsToStandardModel translates parsed port names and groups from the
// RAW platform vocabulary ("ethernet 1/11") into the standard-model
// vocabulary the connector uses everywhere else ("GE11"), when the resolved
// driver declares that translation (driver.PortNamer). Absent that capability
// — most drivers, which already name ports in the standard model, or a
// connector unknown to this binary — it returns the input unchanged.
//
// A group's naming is proven translatable by testing its FIRST and LAST port:
// on every numbering connector shipped today the mapping is affine (same
// trailing number, different prefix), so success on both ends of the range
// carries the whole group. A group that fails is DROPPED with a warning
// naming it — never imported under a name nothing else in the product
// recognizes, and never guessed port by port.
func remapPortsToStandardModel(driverName string, ports map[string]confimport.Port,
	groups []confimport.PortGroup) (map[string]confimport.Port, []confimport.PortGroup, []string) {
	var drv, known = driver.Get(driverName)
	if !known {
		return ports, groups, nil
	}
	var namer, canName = driver.AsPortNamer(drv)
	if !canName {
		return ports, groups, nil
	}

	var outPorts = map[string]confimport.Port{}
	var outGroups = []confimport.PortGroup{}
	var droppedGroups []string
	var g confimport.PortGroup
	for _, g = range groups {
		var first, okFirst = namer.StandardPortName(g.Prefix + strconv.Itoa(g.From))
		var last, okLast = namer.StandardPortName(g.Prefix + strconv.Itoa(g.To))
		if !okFirst || !okLast {
			droppedGroups = append(droppedGroups,
				fmt.Sprintf("%s%d-%d", g.Prefix, g.From, g.To))
			continue
		}
		var stdPrefix = strings.TrimSuffix(first, strconv.Itoa(g.From))
		_ = last
		outGroups = append(outGroups, confimport.PortGroup{
			Prefix: stdPrefix, From: g.From, To: g.To,
			SpeedMbps: g.SpeedMbps, PoE: g.PoE, Label: g.Label,
		})
		var n int
		for n = g.From; n <= g.To; n++ {
			var raw = g.Prefix + strconv.Itoa(n)
			var std, ok = namer.StandardPortName(raw)
			if !ok {
				continue
			}
			var p, seen = ports[raw]
			if !seen {
				p = confimport.Port{Mode: "libre"}
			}
			outPorts[std] = p
		}
	}
	var warnings []string
	if len(droppedGroups) > 0 {
		sort.Strings(droppedGroups)
		warnings = append(warnings, fmt.Sprintf(
			"port group(s) outside this connector's standard-model mapping were not "+
				"imported: %s", strings.Join(droppedGroups, ", ")))
	}
	return outPorts, outGroups, warnings
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

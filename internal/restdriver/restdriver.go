// Package restdriver is the controller-side half of the MDP wire: it wraps a
// driver CONTAINER's HTTP endpoint in the [driver.Connector] and
// [driver.Admin] interfaces, and registers the result in the ordinary driver
// registry.
//
// That last part is the design: `ConnectorFor`, `adminFor` and their
// twenty-six call sites do not know this package exists. A device whose
// stored connector name resolves to a loaded external driver takes the wire
// path; everything else is untouched. Phase 4 deletes the in-tree vendor
// packages and this becomes the only path.
//
// # How capability detection works over a wire
//
// The [Admin] type below implements ALL the optional interfaces — there is no
// type assertion across HTTP, so assertions on it always succeed. What keeps
// that honest is the supports list cached at load: a gesture the driver never
// declared answers [driver.ErrNotImplemented] LOCALLY, with no wire call and
// no session spent. Detection-only call sites migrated to [driver.Has] in the
// contract-hardening pass for exactly this moment.
//
// # The SOCKS credential
//
// A session's device access is minted per session ([socks.Minter]): one host,
// the route the in-process path would have used (read from the Target's own
// dialer, cf. RouteProxyID), a lifetime that outlives the longest lease and
// dies with Close. The driver container receives address+username+password in
// the session body and nothing else — it cannot reach the switch next door.
package restdriver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/socks"
	"meshcraft/wire/mdpv1"
)

// Runtime is what every loaded driver shares: the minter that scopes device
// access, and the SOCKS endpoint AS THE CONTAINERS REACH IT (the advertise
// address, not the bind).
type Runtime struct {
	Minter         *socks.Minter
	SocksAdvertise string
	// Client is the HTTP client used for every call. Long per-op timeouts are
	// the DRIVER's business (a 90-second paged read is legitimate); this
	// client's timeout is the transport ceiling.
	Client *http.Client
}

// credentialTTL outlives the longest session lease: HTTP-transported drivers
// re-dial the device on every request, so the SOCKS credential must live as
// long as the session possibly can. Close revokes it early; a crashed
// controller lets it expire.
const credentialTTL = 45 * time.Minute

// leaseSeconds is what sessions ask for. Ops renew implicitly; a controller
// that dies stops renewing and the driver's reaper hands the slot back.
const leaseSeconds = 300

// routeIdentifier is what a Target's dialer answers when its connections
// leave from a proxy. Defined structurally here so this package depends on
// the API layer's dialer without importing it.
type routeIdentifier interface{ RouteProxyID() string }

// Loaded describes one connector Load registered — what the driver host
// stores so the UI never pays a driver round trip for a capability view.
type Loaded struct {
	Name     string
	Protocol int
	Support  mdpv1.ConnectorSupport
	Info     mdpv1.Info
}

// LoadAll connects to each driver endpoint, refuses protocol mismatches, and
// registers every connector the images declare. Errors name the endpoint —
// one broken driver must not keep the seven others from loading.
func LoadAll(ctx context.Context, endpoints []string, rt Runtime) []error {
	var errs []error
	var ep string
	for _, ep = range endpoints {
		var _, err = Load(ctx, ep, rt)
		if err != nil {
			errs = append(errs, fmt.Errorf("driver endpoint %s: %w", ep, err))
		}
	}
	return errs
}

// Load registers the connectors one endpoint serves and reports them.
func Load(ctx context.Context, endpoint string, rt Runtime) ([]Loaded, error) {
	if rt.Client == nil {
		rt.Client = &http.Client{Timeout: 10 * time.Minute}
	}
	var c = &client{base: strings.TrimRight(endpoint, "/"), rt: rt}

	var info mdpv1.ProtocolInfo
	var err = c.get(ctx, "/protocol", nil, &info)
	if err != nil {
		return nil, fmt.Errorf("protocol handshake: %w", err)
	}
	if info.Protocol != mdpv1.Protocol {
		// Refuse, never degrade: a half-understood contract writes
		// half-understood configuration.
		return nil, fmt.Errorf("protocol %d, this controller speaks %d: refused",
			info.Protocol, mdpv1.Protocol)
	}
	var sup mdpv1.SupportsResponse
	if err = c.get(ctx, "/supports", nil, &sup); err != nil {
		return nil, fmt.Errorf("supports: %w", err)
	}
	var out []Loaded
	var one mdpv1.ConnectorSupport
	for _, one = range sup.Connectors {
		var conn = &Connector{client: c, name: one.Name, support: one}
		if err = c.get(ctx, "/info", url.Values{"connector": {one.Name}}, &conn.info); err != nil {
			return nil, fmt.Errorf("info %s: %w", one.Name, err)
		}
		driver.Register(conn)
		out = append(out, Loaded{Name: one.Name, Protocol: info.Protocol,
			Support: one, Info: conn.info})
	}
	return out, nil
}

// --- HTTP plumbing ---

type client struct {
	base string
	rt   Runtime
}

func (c *client) get(ctx context.Context, path string, q url.Values, out any) error {
	var u = c.base + mdpv1.PathPrefix + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var req, err = http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return c.do(req, out, "")
}

func (c *client) post(ctx context.Context, path string, in, out any, connector string) error {
	var body bytes.Buffer
	if in != nil {
		var err = json.NewEncoder(&body).Encode(in)
		if err != nil {
			return err
		}
	}
	var req, err = http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+mdpv1.PathPrefix+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out, connector)
}

func (c *client) delete(ctx context.Context, path string) error {
	var req, err = http.NewRequestWithContext(ctx, http.MethodDelete,
		c.base+mdpv1.PathPrefix+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, nil, "")
}

// wireError keeps the envelope available to callers that need more than the
// rebuilt error (the transcript on a refused authentication).
type wireError struct {
	body mdpv1.ErrorBody
	err  error
}

func (w *wireError) Error() string { return w.err.Error() }
func (w *wireError) Unwrap() error { return w.err }

func (c *client) do(req *http.Request, out any, connector string) error {
	var res, err = c.rt.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var raw []byte
	raw, err = io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var env mdpv1.ErrorEnvelope
		if json.Unmarshal(raw, &env) != nil || env.Error.Code == "" {
			return fmt.Errorf("driver answered %d: %s", res.StatusCode, firstLine(string(raw)))
		}
		return &wireError{body: env.Error, err: env.Error.ToError(connector)}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func firstLine(s string) string {
	var i = strings.IndexByte(s, '\n')
	if i >= 0 {
		return s[:i]
	}
	return s
}

// --- Connector ---

// Connector is one wire-backed connector, registered under its declared name.
type Connector struct {
	client  *client
	name    string
	info    mdpv1.Info
	support mdpv1.ConnectorSupport

	mu     sync.Mutex
	caps   map[string]mdpv1.Capabilities
	writes map[string]bool
}

func (c *Connector) Info() mdpv1.Info { return c.info }

// Supports answers from the declaration cached at load — the whole point of
// the gesture model: no wire call, no session, no guess.
func (c *Connector) Supports() []mdpv1.Gesture { return c.support.Gestures }

func (c *Connector) has(g mdpv1.Gesture) bool {
	var got mdpv1.Gesture
	for _, got = range c.support.Gestures {
		if got == g {
			return true
		}
	}
	return false
}

// Capabilities is model-dependent and immutable per build: cached per model.
// A wire failure reads as the zero value — the project rule that an unknown
// capability is an absent one, applied to a transport error.
func (c *Connector) Capabilities(model string) mdpv1.Capabilities {
	c.mu.Lock()
	if c.caps == nil {
		c.caps = map[string]mdpv1.Capabilities{}
	}
	var cached, ok = c.caps[model]
	c.mu.Unlock()
	if ok {
		return cached
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out mdpv1.Capabilities
	var err = c.client.get(ctx, "/capabilities",
		url.Values{"connector": {c.name}, "model": {model}}, &out)
	if err != nil {
		return mdpv1.Capabilities{}
	}
	c.mu.Lock()
	c.caps[model] = out
	c.mu.Unlock()
	return out
}

func (c *Connector) Fingerprint(ev mdpv1.Evidence) int {
	var ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out mdpv1.FingerprintResponse
	var err = c.client.post(ctx, "/fingerprint",
		mdpv1.FingerprintRequest{Connector: c.name, Evidence: ev}, &out, c.name)
	if err != nil {
		return 0
	}
	return out.Score
}

func (c *Connector) ResetProcedure(model string) mdpv1.ResetProcedure {
	var ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out mdpv1.ResetProcedure
	var err = c.client.get(ctx, "/reset-procedure",
		url.Values{"connector": {c.name}, "model": {model}}, &out)
	if err != nil {
		return mdpv1.ResetProcedure{}
	}
	return out
}

// WritesExtra is consulted before every apply: cached per name, static per
// build. A wire failure reads as false — never promise a write.
func (c *Connector) WritesExtra(name string) bool {
	c.mu.Lock()
	if c.writes == nil {
		c.writes = map[string]bool{}
	}
	var cached, ok = c.writes[name]
	c.mu.Unlock()
	if ok {
		return cached
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out struct {
		OK bool `json:"ok"`
	}
	var err = c.client.get(ctx, "/writes-extra",
		url.Values{"connector": {c.name}, "name": {name}}, &out)
	if err != nil {
		return false
	}
	c.mu.Lock()
	c.writes[name] = out.OK
	c.mu.Unlock()
	return out.OK
}

// mint scopes one credential for one target. The route comes from the
// Target's own dialer: the wire path leaves from EXACTLY the proxy the
// in-process path would have used.
func (c *Connector) mint(tgt mdpv1.Target) (mdpv1.SOCKSAccess, string, error) {
	var proxyID string
	var ri, routed = tgt.Dialer.(routeIdentifier)
	if routed {
		proxyID = ri.RouteProxyID()
	}
	var raw = make([]byte, 8)
	_, _ = rand.Read(raw)
	var sessionID = hex.EncodeToString(raw)
	var cred, err = c.client.rt.Minter.Mint(sessionID, tgt.Host, proxyID, tgt.Host, credentialTTL)
	if err != nil {
		return mdpv1.SOCKSAccess{}, "", err
	}
	return mdpv1.SOCKSAccess{
		Address:  c.client.rt.SocksAdvertise,
		Username: cred.Username,
		Password: cred.Password,
	}, cred.Username, nil
}

// Authenticate opens the wire session. The candidate order crosses verbatim;
// the driver side restates the same budget rules the in-process contract
// documents.
func (c *Connector) Authenticate(ctx context.Context, tgt mdpv1.Target,
	cands []mdpv1.Candidate) (mdpv1.Admin, error) {

	var access, minted, err = c.mint(tgt)
	if err != nil {
		return nil, err
	}
	var out mdpv1.SessionResponse
	err = c.client.post(ctx, "/sessions", mdpv1.SessionRequest{
		Connector:    c.name,
		Target:       mdpv1.TargetSpec{Host: tgt.Host, Model: tgt.Model, PortPrefix: tgt.PortPrefix},
		Candidates:   cands,
		SOCKS:        access,
		LeaseSeconds: leaseSeconds,
	}, &out, c.name)
	if err != nil {
		c.client.rt.Minter.Revoke(minted)
		// A refused authentication still carries its transcript — the only
		// diagnostic half the fleet has. The stub admin serves it to the
		// caller exactly as an in-tree connector's failed Admin would.
		var we *wireError
		if errors.As(err, &we) {
			return &Admin{conn: c, transcript: we.body.Transcript}, errors.New(we.body.Detail)
		}
		return nil, err
	}
	return &Admin{
		conn:       c,
		id:         out.SessionID,
		minted:     minted,
		account:    out.Account,
		accepted:   out.Accepted,
		transcript: out.Transcript,
	}, nil
}

// --- Connector-level optional interfaces ---

// StormControlCaps answers from the wire; absent gesture = zero caps, which
// every screen reads as "cannot".
func (c *Connector) StormControlCaps() mdpv1.StormControlCaps {
	if !c.has(mdpv1.GestureStormControlCaps) {
		return mdpv1.StormControlCaps{}
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out mdpv1.StormControlCaps
	if c.client.get(ctx, "/storm-caps", url.Values{"connector": {c.name}}, &out) != nil {
		return mdpv1.StormControlCaps{}
	}
	return out
}

func (c *Connector) StandardPortName(deviceName string) (string, bool) {
	if !c.has(mdpv1.GesturePortName) {
		return "", false
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out mdpv1.PortNameResponse
	var err = c.client.post(ctx, "/standard-port-name",
		mdpv1.PortNameRequest{Connector: c.name, DeviceName: deviceName}, &out, c.name)
	if err != nil {
		return "", false
	}
	return out.Name, out.OK
}

func (c *Connector) WebUIBase(host string) string {
	if !c.has(mdpv1.GestureWebUI) {
		return ""
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out mdpv1.WebUIResponse
	if c.client.get(ctx, "/webui",
		url.Values{"connector": {c.name}, "host": {host}}, &out) != nil {
		return ""
	}
	return out.Base
}

func (c *Connector) WebUIAuth() mdpv1.WebAuth {
	var ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out mdpv1.WebUIResponse
	if c.client.get(ctx, "/webui",
		url.Values{"connector": {c.name}, "host": {""}}, &out) != nil {
		return ""
	}
	return out.Auth
}

func (c *Connector) Identify(ctx context.Context, tgt mdpv1.Target,
	cands []mdpv1.Candidate) (mdpv1.Identity, error) {

	if !c.has(mdpv1.GestureIdentify) {
		return mdpv1.Identity{}, mdpv1.ErrNotImplemented
	}
	var access, minted, err = c.mint(tgt)
	if err != nil {
		return mdpv1.Identity{}, err
	}
	defer c.client.rt.Minter.Revoke(minted)
	var out struct {
		Identity mdpv1.Identity `json:"identity"`
		Error    string         `json:"error,omitempty"`
	}
	err = c.client.post(ctx, "/identify", mdpv1.SessionlessRequest{
		Connector:  c.name,
		Target:     mdpv1.TargetSpec{Host: tgt.Host, Model: tgt.Model, PortPrefix: tgt.PortPrefix},
		Candidates: cands,
		SOCKS:      access,
	}, &out, c.name)
	if err != nil {
		return mdpv1.Identity{}, err
	}
	// The partial-identity rule: what was established travels with the
	// reason the driver stopped.
	if out.Error != "" {
		return out.Identity, errors.New(out.Error)
	}
	return out.Identity, nil
}

func (c *Connector) Onboard(ctx context.Context, tgt mdpv1.Target) (mdpv1.OnboardResult, error) {
	if !c.has(mdpv1.GestureOnboard) {
		return mdpv1.OnboardResult{}, mdpv1.ErrNotImplemented
	}
	var access, minted, err = c.mint(tgt)
	if err != nil {
		return mdpv1.OnboardResult{}, err
	}
	defer c.client.rt.Minter.Revoke(minted)
	var out mdpv1.OnboardResult
	err = c.client.post(ctx, "/onboard", mdpv1.SessionlessRequest{
		Connector: c.name,
		Target:    mdpv1.TargetSpec{Host: tgt.Host, Model: tgt.Model, PortPrefix: tgt.PortPrefix},
		SOCKS:     access,
	}, &out, c.name)
	if err != nil {
		return mdpv1.OnboardResult{}, err
	}
	return out, nil
}

// Compile-time: the wire connector satisfies the contract and every
// connector-level optional interface.
var (
	_ driver.Connector           = (*Connector)(nil)
	_ mdpv1.Identifier           = (*Connector)(nil)
	_ mdpv1.Onboarder            = (*Connector)(nil)
	_ mdpv1.PortNamer            = (*Connector)(nil)
	_ mdpv1.WebUIAddresser       = (*Connector)(nil)
	_ mdpv1.StormControlCapabler = (*Connector)(nil)
)

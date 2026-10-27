// Package unifi reads a UniFi OS controller, and asks it to perform exactly
// four gestures: adopt, forget, reprovision (ADR-0025) and edit-ssid
// (ADR-0029).
//
// THE OLD PROMISE AND THE ONE THAT REPLACED IT. This package used to have no
// write method at all, which is how ADR-0015 §5 kept "meshcraft configures
// nothing through UniFi" — a reviewer checked it by reading the method set.
// ADR-0025 reopened that point, so the guarantee had to be replaced by one of
// the same kind rather than by a comment. It is in gestures.go: `Do` is the
// only method here that writes, its argument is a `Gesture` whose field is
// unexported and which has no literal form, so no caller outside this package
// can build one. The set of writes meshcraft can ask of UniFi is therefore
// closed at compile time, and you count it by counting the four `var` lines
// that declare them.
//
// What is NOT here matters as much: meshcraft stores no intention about any of
// it, and pushes none. "Reprovision" asks the controller to reassert ITS own
// configuration (ADR-0025 §2). An SSID edit is a one-shot — read, change,
// write, read back — kept nowhere afterwards, which is why ADR-0029 is an
// editor and not a convergence loop.
//
// TWO APIS, ONE CONTROLLER, and this is the awkward fact the package exists to
// hide. UniFi Network 9+ ships an "integration" API that is documented, keyed
// by `X-API-KEY`, and paginated: /info, /sites, /sites/{id}/devices, the detail
// of one device, /statistics/latest, /clients. It does NOT expose SSIDs —
// /sites/{id}/wlans answers 404 on the controller this was built against
// (application version 10.6.109). SSIDs therefore come from the LEGACY API,
// /proxy/network/api/s/{ref}/rest/wlanconf, which is indexed by the site's
// short `internalReference` rather than by its UUID. That is the whole reason
// SiteExtension stores both identifiers.
//
// The legacy half is not under any contract and may break at a UniFi upgrade.
// It is isolated in ListSSIDs for exactly that reason, and a caller is expected
// to degrade a screen rather than fail it when that call errors.
package unifi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout bounds every call. An extension is a third-party dependency on
// the controller's own request path (ADR-0015, Consequences): without a hard
// deadline, a UniFi controller that accepts a connection and then stops talking
// would hold a meshcraft handler forever.
const DefaultTimeout = 15 * time.Second

// maxBody caps a response read. A controller answering an unbounded stream must
// not be able to exhaust the controller's memory.
const maxBody = 8 << 20

// pageLimit is the page size asked of the integration API. Its default is 25,
// which silently truncates a site with more boxes than that — the kind of bug
// that looks like "the import misses devices" months later.
const pageLimit = 200

// maxPages bounds pagination so a controller that always reports more pages
// cannot loop us forever.
const maxPages = 50

// Client is one configured UniFi OS controller.
type Client struct {
	// BaseURL without a trailing slash. UniFi's nginx answers 400 Bad Request
	// to the "//proxy/network/..." that a naive join produces, and the error
	// names neither the path nor the cause — so the normalization is not
	// cosmetic, it is the difference between working and an hour lost.
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// Option configures a Client.
type Option func(*clientOpts)

type clientOpts struct {
	verifyTLS bool
	dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	timeout   time.Duration
}

// WithInsecureTLS disables certificate verification. On-premise UniFi OS ships
// a self-signed certificate, so this is an operational need and not laziness —
// which is why it is a per-extension choice recorded in the database rather
// than a global relaxation.
func WithInsecureTLS(insecure bool) Option {
	return func(o *clientOpts) { o.verifyTLS = !insecure }
}

// WithDialer routes every connection through a custom dialer — in practice the
// SOCKS5 dialer of the proxy posted on a site, for a UniFi controller that is
// not reachable from the meshcraft controller itself. This is the mechanism
// behind "reachability is a configuration, not an architecture" (ADR-0015 §3).
func WithDialer(dial func(ctx context.Context, network, addr string) (net.Conn, error)) Option {
	return func(o *clientOpts) { o.dial = dial }
}

// WithTimeout overrides DefaultTimeout.
func WithTimeout(d time.Duration) Option {
	return func(o *clientOpts) {
		if d > 0 {
			o.timeout = d
		}
	}
}

// New builds a client. `base` is normalized here, once, so no call site has to
// remember the trailing-slash trap.
func New(base, token string, opts ...Option) (*Client, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil, errors.New("unifi: empty base URL")
	}
	var u, err = url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("unifi: bad base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unifi: base URL must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("unifi: base URL has no host")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("unifi: empty token")
	}

	var o = clientOpts{verifyTLS: true, timeout: DefaultTimeout}
	var opt Option
	for _, opt = range opts {
		opt(&o)
	}

	var tr = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: !o.verifyTLS}, //nolint:gosec // per-extension operator choice, cf. WithInsecureTLS
		DialContext:     o.dial,
		// A controller is contacted a handful of times per import; keeping idle
		// connections to a third party buys nothing and holds sockets open.
		DisableKeepAlives: true,
	}
	return &Client{
		BaseURL: base,
		Token:   token,
		HTTP:    &http.Client{Transport: tr, Timeout: o.timeout},
	}, nil
}

// --- Wire shapes, exactly as the controller sends them ---

type page struct {
	Offset     int             `json:"offset"`
	Limit      int             `json:"limit"`
	Count      int             `json:"count"`
	TotalCount int             `json:"totalCount"`
	Data       json.RawMessage `json:"data"`
}

// Site is one site of the controller. `ID` is the stable key; `Name` is a label
// that will be renamed (ADR-0015 §6). `InternalReference` is the short form the
// legacy SSID endpoint needs.
type Site struct {
	ID                string `json:"id"`
	InternalReference string `json:"internalReference"`
	Name              string `json:"name"`
}

// Device is the integration API's view of one box. The `features` and
// `interfaces` fields change SHAPE between the list and the detail call — an
// array of names in the list, an object in the detail — which is why they are
// json.RawMessage here and decoded by the two dedicated types below.
type Device struct {
	ID              string          `json:"id"`
	MACAddress      string          `json:"macAddress"`
	IPAddress       string          `json:"ipAddress"`
	Name            string          `json:"name"`
	Model           string          `json:"model"`
	State           string          `json:"state"`
	Supported       bool            `json:"supported"`
	FirmwareVersion string          `json:"firmwareVersion"`
	AdoptedAt       string          `json:"adoptedAt"`
	ProvisionedAt   string          `json:"provisionedAt"`
	ConfigurationID string          `json:"configurationId"`
	Features        json.RawMessage `json:"features"`
	Interfaces      json.RawMessage `json:"interfaces"`
}

// DeviceDetail carries the per-device interfaces the list call omits: the
// radios, which is the only place the band, channel and width are available.
type DeviceDetail struct {
	Device
	Decoded DeviceInterfaces `json:"-"`
}

// DeviceInterfaces is the detail call's `interfaces` object.
type DeviceInterfaces struct {
	Ports  []DevicePort  `json:"ports"`
	Radios []DeviceRadio `json:"radios"`
}

// DevicePort is one copper port.
type DevicePort struct {
	Idx          int    `json:"idx"`
	State        string `json:"state"`
	Connector    string `json:"connector"`
	SpeedMbps    int    `json:"speedMbps"`
	MaxSpeedMbps int    `json:"maxSpeedMbps"`
}

// DeviceRadio is one 802.11 radio.
type DeviceRadio struct {
	WLANStandard    string  `json:"wlanStandard"`
	FrequencyGHz    float64 `json:"frequencyGHz"`
	ChannelWidthMHz int     `json:"channelWidthMHz"`
	Channel         int     `json:"channel"`
}

// SSID is one service set, read from the legacy API.
type SSID struct {
	ID       string   `json:"_id"`
	Name     string   `json:"name"`
	Enabled  bool     `json:"enabled"`
	Security string   `json:"security"`
	WPAMode  string   `json:"wpa_mode"`
	Bands    []string `json:"wlan_bands"`
}

// --- Errors a caller must be able to tell apart ---

// ErrRemoteSiteGone means the controller ANSWERED and the requested site was
// not in its list. Deliberately distinct from a transport failure: only this
// one orphans a pairing, because a network outage must not erase somebody's
// correlation work (ADR-0015 §7).
var ErrRemoteSiteGone = errors.New("unifi: site absent from the controller's list")

// ErrUnauthorized is a token the controller refused — a 401/403. Worth its own
// error because the operator's fix is to paste a new token, not to check the
// network.
var ErrUnauthorized = errors.New("unifi: controller refused the token")

// HTTPError carries a status the caller may want to show verbatim.
type HTTPError struct {
	Status int
	Path   string
	Body   string
}

func (e *HTTPError) Error() string {
	var body = e.Body
	if len(body) > 200 {
		body = body[:200]
	}
	return fmt.Sprintf("unifi: %s: HTTP %d: %s", e.Path, e.Status, strings.TrimSpace(body))
}

// --- Calls ---

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// do is the one place a request is built, so the API key, the body limit and
// the status mapping cannot diverge between a read and a gesture. `body` is
// already-encoded JSON, or nil.
func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	var req, err = http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-KEY", c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	var resp *http.Response
	resp, err = c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("unifi: %s: %w", path, err)
	}
	defer resp.Body.Close()

	var answer []byte
	answer, err = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("unifi: %s: reading body: %w", path, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d)", ErrUnauthorized, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return &HTTPError{Status: resp.StatusCode, Path: path, Body: string(answer)}
	}
	if out == nil {
		return nil
	}
	if err = json.Unmarshal(answer, out); err != nil {
		return fmt.Errorf("unifi: %s: decoding body: %w", path, err)
	}
	return nil
}

// Version returns the Network application version — the cheapest proof that the
// base URL and the token are both good, which is what a "test" button needs.
func (c *Client) Version(ctx context.Context) (string, error) {
	var out struct {
		ApplicationVersion string `json:"applicationVersion"`
	}
	var err = c.get(ctx, "/proxy/network/integration/v1/info", &out)
	if err != nil {
		return "", err
	}
	return out.ApplicationVersion, nil
}

// ListSites returns every site of the controller, paginated to the end.
func (c *Client) ListSites(ctx context.Context) ([]Site, error) {
	var out []Site
	var err = c.pages(ctx, "/proxy/network/integration/v1/sites", func(raw json.RawMessage) (int, error) {
		var batch []Site
		var decErr = json.Unmarshal(raw, &batch)
		if decErr != nil {
			return 0, decErr
		}
		out = append(out, batch...)
		return len(batch), nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FindSite resolves a pairing by its STABLE id and returns the site as the
// controller currently describes it — which is how a renamed site is noticed
// without the pairing breaking.
//
// It returns ErrRemoteSiteGone only when the list was obtained and the id was
// absent from it. A transport error comes back as itself, so the caller can
// tell "site deleted" from "controller down" and orphan only the former.
func (c *Client) FindSite(ctx context.Context, siteID string) (Site, error) {
	var sites, err = c.ListSites(ctx)
	if err != nil {
		return Site{}, err
	}
	var s Site
	for _, s = range sites {
		if s.ID == siteID {
			return s, nil
		}
	}
	return Site{}, fmt.Errorf("%w: %s", ErrRemoteSiteGone, siteID)
}

// ListDevices returns every box of a site, each enriched with its DETAIL call —
// because the list call carries `interfaces` as a list of NAMES ("ports",
// "radios") and only the detail call carries the radios themselves. One extra
// request per box, which is the price of knowing a channel.
//
// A box whose detail call fails is still returned, with whatever the list knew:
// losing a whole inventory because one access point was rebooting would be the
// wrong trade.
func (c *Client) ListDevices(ctx context.Context, siteID string) ([]DeviceDetail, error) {
	var base = "/proxy/network/integration/v1/sites/" + url.PathEscape(siteID) + "/devices"
	var listed []Device
	var err = c.pages(ctx, base, func(raw json.RawMessage) (int, error) {
		var batch []Device
		var decErr = json.Unmarshal(raw, &batch)
		if decErr != nil {
			return 0, decErr
		}
		listed = append(listed, batch...)
		return len(batch), nil
	})
	if err != nil {
		return nil, err
	}

	var out = make([]DeviceDetail, 0, len(listed))
	var d Device
	for _, d = range listed {
		var full = DeviceDetail{Device: d}
		var detail DeviceDetail
		var detErr = c.get(ctx, base+"/"+url.PathEscape(d.ID), &detail)
		if detErr == nil {
			// The detail answer wins on every scalar: it is the fresher read
			// and the only one carrying adoptedAt / configurationId.
			full = detail
			if len(detail.Interfaces) > 0 {
				var iface DeviceInterfaces
				if json.Unmarshal(detail.Interfaces, &iface) == nil {
					full.Decoded = iface
				}
			}
		}
		out = append(out, full)
	}
	return out, nil
}

// ListSSIDs reads the site's service sets from the LEGACY API, which is indexed
// by the short reference and not by the site UUID — hence `ref`.
//
// This endpoint is outside any contract UniFi publishes and is the most likely
// thing here to break at an upgrade. A caller degrades its screen on error; it
// does not fail the import (ADR-0015, Consequences).
func (c *Client) ListSSIDs(ctx context.Context, ref string) ([]SSID, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, errors.New("unifi: no site reference; SSIDs need the legacy short name")
	}
	var out struct {
		Meta struct {
			RC string `json:"rc"`
		} `json:"meta"`
		Data []SSID `json:"data"`
	}
	var err = c.get(ctx, "/proxy/network/api/s/"+url.PathEscape(ref)+"/rest/wlanconf", &out)
	if err != nil {
		return nil, err
	}
	if out.Meta.RC != "" && out.Meta.RC != "ok" {
		return nil, fmt.Errorf("unifi: wlanconf refused: rc=%s", out.Meta.RC)
	}
	return out.Data, nil
}

// pages walks the integration API's offset pagination. `consume` reports how
// many items it decoded, which is what ends the walk — trusting `totalCount`
// alone would loop forever on a controller that reports one and sends none.
func (c *Client) pages(ctx context.Context, path string, consume func(json.RawMessage) (int, error)) error {
	var offset int
	var i int
	for i = 0; i < maxPages; i++ {
		var p page
		var q = fmt.Sprintf("%s?offset=%d&limit=%d", path, offset, pageLimit)
		var err = c.get(ctx, q, &p)
		if err != nil {
			return err
		}
		var n int
		n, err = consume(p.Data)
		if err != nil {
			return fmt.Errorf("unifi: %s: %w", path, err)
		}
		offset += n
		if n == 0 || offset >= p.TotalCount {
			return nil
		}
	}
	return fmt.Errorf("unifi: %s: pagination did not terminate after %d pages", path, maxPages)
}

// --- Translation into meshcraft's neutral vocabulary ---

// ClassOf decides what an imported box IS, from the `features` the controller
// advertises. The feature list is the right source: UniFi's `model` string
// ("AC Pro") says nothing parseable, and its legacy `type` ("uap", "usw") is
// only on the other API.
//
// Anything unrecognised becomes `other` rather than being guessed into a class.
// A box filed as a switch when it is not would be the first step towards
// someone expecting to configure it.
func ClassOf(features json.RawMessage) string {
	var names = featureNames(features)
	switch {
	case names["accessPoint"]:
		return "access_point"
	case names["switching"]:
		return "switch"
	case names["gateway"]:
		return "gateway"
	}
	return "other"
}

// featureNames copes with the two shapes of `features`: `["accessPoint"]` in a
// list answer, `{"accessPoint":{}}` in a detail answer. Both appear on the same
// controller, in the same import.
func featureNames(raw json.RawMessage) map[string]bool {
	var out = map[string]bool{}
	if len(raw) == 0 {
		return out
	}
	var asList []string
	if json.Unmarshal(raw, &asList) == nil {
		var n string
		for _, n = range asList {
			out[n] = true
		}
		return out
	}
	var asObj map[string]json.RawMessage
	if json.Unmarshal(raw, &asObj) == nil {
		var k string
		for k = range asObj {
			out[k] = true
		}
	}
	return out
}

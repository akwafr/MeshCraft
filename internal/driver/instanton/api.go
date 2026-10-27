package instanton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// Instant On provisioning API, observed on real hardware
// (1930 48-port, firmware 3.4.0.0) by reading the Angular bundle of its
// UI — no guesswork; each endpoint is the one the UI calls.
//
//	base : https://<ip>:4443/api      (80 and 443 redirect to 4443)
//
//	GET  /portalConnectionStatus  → management mode and onboarding state
//	GET  /systemInfo              → model, serial, MAC, firmware, port count
//	GET  /connectivity            → management VLAN, addressing
//	POST /connectivity            → applies addressing
//	PUT  /managementPersonality   {"locallyManaged":true}  ← "Configure Locally"
//	POST /login                   {"deviceWebUiToken":"…"}
//	POST /logout
//	POST /restart                 {}
//
// Authentication is by session cookie; when not onboarded, the reads above
// respond without a session.
//
// NOTE: this device ALSO exposes an older API via
// `?opcode=…&cmd=…`, the classic management UI (VLAN, ports, STP). Both
// coexist: `/api/*` handles provisioning; the older API handles network
// configuration. Do not confuse them.
const (
	// ProvisioningPort is the provisioning UI port.
	ProvisioningPort = 4443

	pathPortalStatus = "/api/portalConnectionStatus"
	pathSystemInfo   = "/api/systemInfo"
	pathConnectivity = "/api/connectivity"
	pathMgmtPersonal = "/api/managementPersonality"
	pathLogin        = "/api/login"
	pathRestart      = "/api/restart"
)

// BaseURL builds the provisioning UI URL for an address.
func BaseURL(host string) string {
	return fmt.Sprintf("https://%s:%d", host, ProvisioningPort)
}

// PortalStatus is the /portalConnectionStatus response.
type PortalStatus struct {
	OnboardingStatus    string `json:"onboardingStatus"` // RETRY | ERROR | UNKNOWN | …
	OnboardingMessage   string `json:"onboardingStatusMessage"`
	ServerReachable     bool   `json:"onboardingServerReachable"`
	CouldNotComplete    bool   `json:"onboardingCouldNotComplete"`
	PeriodOver          bool   `json:"onboardingPeriodOver"`
	ManagementMode      string `json:"managementMode"` // DEFAULT | CLOUD
	CanEditConnectivity bool   `json:"canEditConnectivitySettings"`
}

// SystemInfo is the /systemInfo response.
type SystemInfo struct {
	CurrentTime     string `json:"currentTime"`
	FirmwareVersion string `json:"firmwareVersion"`
	BuildNumber     string `json:"buildNumber"`
	NumberOfPorts   int    `json:"numberOfPorts"`
	MACAddress      string `json:"macAddress"`
	SerialNumber    string `json:"serialNumber"`
	Model           string `json:"model"`
	BootVersion     string `json:"bootVersion"`
}

// Connectivity is the /connectivity response — and the body expected on POST.
// Field names are the device's, not ours: the connector translates to the
// standard model, not the other way around.
type Connectivity struct {
	ManagementVlan       int    `json:"managementVlan"`
	ManagementVlanTagged bool   `json:"managementVlanTagged"`
	NonUplinkPortsAccess string `json:"nonUplinkPortsAccess"` // OPENED | …
	IPv4AddressingMode   string `json:"ipV4AddressingMode"`   // DHCP | STATIC
	IPv4Address          string `json:"ipV4Address,omitempty"`
	IPv4SubnetMask       string `json:"ipV4SubnetMask,omitempty"`
	IPv4Gateway          string `json:"ipV4GatewayAddress,omitempty"`
	DNSPrimary           string `json:"dnsPrimary,omitempty"`
	DNSSecondary         string `json:"dnsSecondary,omitempty"`
	UplinkPort           string `json:"uplinkPort,omitempty"`
}

// Client talks to an Instant On provisioning UI.
type Client struct {
	web *driver.WebSession
}

// NewClient opens a session to the device. The Dialer chooses the path
// (direct or via a proxy tunnel): the client does not know.
func NewClient(host string, d driver.Dialer) *Client {
	return &Client{web: driver.NewWebSession(BaseURL(host), d, 0)}
}

// --- Reads: no side effects on the device ---

func (c *Client) PortalStatus(ctx context.Context) (PortalStatus, error) {
	var out PortalStatus
	var err = c.getJSON(ctx, pathPortalStatus, &out)
	return out, err
}

func (c *Client) SystemInfo(ctx context.Context) (SystemInfo, error) {
	var out SystemInfo
	var err = c.getJSON(ctx, pathSystemInfo, &out)
	return out, err
}

func (c *Client) Connectivity(ctx context.Context) (Connectivity, error) {
	var out Connectivity
	var err = c.getJSON(ctx, pathConnectivity, &out)
	return out, err
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	var resp, err = c.web.Get(ctx, path)
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return fmt.Errorf("%s: HTTP %d", path, resp.Status)
	}
	if err = json.Unmarshal([]byte(resp.Body), out); err != nil {
		return fmt.Errorf("%s: unreadable response: %w", path, err)
	}
	return nil
}

// StageOf classifies device state from portal status. This decides what
// happens next during adoption.
func StageOf(st PortalStatus) Stage {
	switch {
	case strings.EqualFold(st.ManagementMode, "CLOUD"):
		// The UI considers the device "onboarded" when mode is CLOUD:
		// it is managed by the Instant On portal, not by us.
		return StageCloudManaged
	case st.CanEditConnectivity:
		// DEFAULT mode with editable settings: the choice has not been made.
		return StageChooseMode
	}
	return StageUnknown
}

// ErrWrongStage: the requested action does not match device state.
// Better to refuse than to push an order at the wrong time.
var ErrWrongStage = errors.New("device is not in the expected state for this action")

// ConfigureLocally triggers "Configure Locally": the device stops
// waiting for the cloud portal and becomes locally manageable. This is
// the exact equivalent of the UI button — `PUT /managementPersonality`
// with `{"locallyManaged": true}`.
//
// WRITE, and a durable choice: only a factory reset undoes it
// (cf. ResetProcedure). State is checked before, and the result after.
func (c *Client) ConfigureLocally(ctx context.Context) (PortalStatus, *driver.Response, error) {
	var before, err = c.PortalStatus(ctx)
	if err != nil {
		return before, nil, err
	}
	if StageOf(before) == StageCloudManaged {
		return before, nil, fmt.Errorf("%w: already attached to the Instant On cloud portal", ErrWrongStage)
	}

	var resp *driver.Response
	resp, err = c.web.Put(ctx, pathMgmtPersonal, `{"locallyManaged":true}`)
	if err != nil {
		return before, nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return before, resp, fmt.Errorf("%s: HTTP %d %s", pathMgmtPersonal, resp.Status,
			strings.TrimSpace(resp.Body))
	}
	// Do NOT rely on the status code alone: a 2xx that changes nothing is a
	// silent failure. The caller compares before/after state and sees the
	// raw device response.
	var after, afterErr = c.PortalStatus(ctx)
	return after, resp, afterErr
}

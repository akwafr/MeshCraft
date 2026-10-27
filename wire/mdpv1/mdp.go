package mdpv1

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// The MDP wire envelope — the HTTP half of the Meshcraft Driver Protocol.
//
// Everything in this file crosses the boundary between the controller and a
// driver CONTAINER, possibly written in another language. The Go types are
// the source of truth; contracts/mdp/v1/openapi.yaml documents the same
// shapes for non-Go authors, and the conformance suite validates live
// responses against that document so the two cannot drift apart silently.
//
// # The three rules the envelope carries
//
//   - SESSIONS ARE PINNED, NEVER IMPLIED. Devices hand out five slots and
//     lock accounts out; a driver therefore authenticates ONCE per
//     [SessionRequest] and everything else names the session it spends.
//     Leases bound the damage of a crashed controller: an orphaned session
//     closes itself when the lease runs out, and every op call implicitly
//     renews.
//   - THE DEVICE IS REACHED THROUGH SOCKS, NEVER DIRECTLY. A driver holds no
//     route: [SOCKSAccess] in the request is the only path to the device,
//     scoped to that one host by the controller's minter. This is what makes
//     "any language" true — every language dials SOCKS5.
//   - ERRORS ARE ENVELOPES, NEVER BARE STATUS CODES. [Unsupported.Because] is
//     shown verbatim to operators; a wire that dropped it would turn every
//     refusal into "it failed". [ErrorBody] carries the same distinctions the
//     in-process error types do, both ways.
type mdpDoc struct{} //nolint:unused // anchor for the package-level doc above

// Protocol is the MDP major version this package speaks. A driver refuses a
// mismatched major at the protocol handshake — refuse, never degrade: a
// half-understood contract writes half-understood configuration.
const Protocol = 1

// PathPrefix is where a driver mounts the protocol, version included, so two
// majors can coexist on one container during a migration.
const PathPrefix = "/mdp/v1"

// --- Error envelope ---

// Error codes. A code names WHO said no and whether retrying can help; the
// prose beside it is for humans and is never parsed.
const (
	// ErrCodeNotImplemented: the connector has not coded this gesture yet.
	ErrCodeNotImplemented = "not_implemented"
	// ErrCodeUnsupported: the platform structurally lacks it; `because` is
	// the operator-facing sentence. Maps to [Unsupported].
	ErrCodeUnsupported = "unsupported"
	// ErrCodeAuthFailed: no candidate opened the device; transcript attached.
	ErrCodeAuthFailed = "auth_failed"
	// ErrCodeDeviceError: the device was reached and the conversation failed.
	ErrCodeDeviceError = "device_error"
	// ErrCodeSessionExpired: the lease ran out; the session is gone.
	ErrCodeSessionExpired = "session_expired"
	// ErrCodeSessionPoisoned: a cancelled op left the transport in a state
	// the driver cannot trust (a fed pager mid-page). The session must be
	// closed; nothing on it will succeed.
	ErrCodeSessionPoisoned = "session_poisoned"
	// ErrCodeBusy: the device's session budget is spent. Retry-After applies.
	ErrCodeBusy = "busy"
	// ErrCodeBadRequest: the controller sent something this driver cannot
	// parse. A contract bug, not a device condition.
	ErrCodeBadRequest = "bad_request"
	// ErrCodeProtocolMismatch: majors differ. Nothing proceeds.
	ErrCodeProtocolMismatch = "protocol_mismatch"
)

// ErrorBody is the payload of every non-2xx answer.
type ErrorBody struct {
	Code string `json:"code"`
	// Gesture and Because mirror [Unsupported]: which gesture, and the
	// platform's own reason, verbatim for the operator.
	Gesture string `json:"gesture,omitempty"`
	Because string `json:"because,omitempty"`
	// Transcript rides along when the failure has one (auth, device errors):
	// on a device with no other diagnostic path it is the only way to
	// understand a failure without going on site.
	Transcript []Step `json:"transcript,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// ErrorEnvelope wraps [ErrorBody] — the top-level shape of a failed response.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// ToError rebuilds the in-process error a driver-side implementation
// returned, so the controller-side caller sees EXACTLY what an in-tree
// driver would have produced: errors.Is(err, ErrNotImplemented) keeps
// answering true, and an [Unsupported] keeps its fields.
func (b ErrorBody) ToError(connector string) error {
	switch b.Code {
	case ErrCodeNotImplemented:
		return ErrNotImplemented
	case ErrCodeUnsupported:
		return &Unsupported{Connector: connector, Gesture: b.Gesture, Because: b.Because}
	default:
		return errors.New(b.Detail)
	}
}

// ErrorBodyOf classifies an in-process error into its wire form — the
// driver-side half of the round trip [ErrorBody.ToError] completes.
func ErrorBodyOf(err error) ErrorBody {
	var unsup *Unsupported
	if errors.As(err, &unsup) {
		return ErrorBody{Code: ErrCodeUnsupported,
			Gesture: unsup.Gesture, Because: unsup.Because}
	}
	if errors.Is(err, ErrNotImplemented) {
		return ErrorBody{Code: ErrCodeNotImplemented}
	}
	return ErrorBody{Code: ErrCodeDeviceError, Detail: err.Error()}
}

// --- Static endpoints ---

// ProtocolInfo answers GET /mdp/v1/protocol — the handshake. The controller
// refuses a driver whose major differs from [Protocol], and says so in the
// build log rather than degrading.
type ProtocolInfo struct {
	Protocol int `json:"protocol"`
	Driver   struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"driver"`
}

// ConnectorSupport is one connector's static declaration, cached by the
// controller at load time: the wire form of [Connector.Supports] and
// [Info.IdentifyNeedsCredentials]. There is no type assertion across a wire —
// this list is the ONLY capability answer an external driver has.
type ConnectorSupport struct {
	Name     string    `json:"name"`
	Gestures []Gesture `json:"gestures"`
	Identify struct {
		NeedsCredentials bool `json:"needs_credentials"`
	} `json:"identify"`
}

// SupportsResponse answers GET /mdp/v1/supports. One image may carry several
// connectors (a vendor family), each with its own gesture list.
type SupportsResponse struct {
	Connectors []ConnectorSupport `json:"connectors"`
}

// FingerprintRequest asks "is this device yours?" for POST /mdp/v1/fingerprint.
type FingerprintRequest struct {
	Connector string   `json:"connector"`
	Evidence  Evidence `json:"evidence"`
}

// FingerprintResponse is the 0–100 score, same scale as [Driver.Fingerprint].
type FingerprintResponse struct {
	Score int `json:"score"`
}

// --- Reaching the device ---

// TargetSpec is [Target] minus the Dialer: what serializes. The driver builds
// its own Dialer from [SOCKSAccess].
type TargetSpec struct {
	Host       string `json:"host"`
	Model      string `json:"model,omitempty"`
	PortPrefix string `json:"port_prefix,omitempty"`
}

// SOCKSAccess is the one road to the device: the controller's SOCKS5
// endpoint and a credential minted for exactly this session's host. The
// driver treats it as opaque and NEVER retries authentication with variations
// — the credential either opens the route or the session is over.
type SOCKSAccess struct {
	// Address is host:port, reachable from the driver container's network.
	Address  string `json:"address"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// --- Sessions ---

// SessionRequest opens a session: POST /mdp/v1/sessions. ONE Authenticate,
// candidates tried in the order given and never replayed — the same sacred
// order [Connector.Authenticate] documents, restated here because a driver in
// another language cannot read that doc comment's compiler.
type SessionRequest struct {
	Connector    string      `json:"connector"`
	Target       TargetSpec  `json:"target"`
	Candidates   []Candidate `json:"candidates"`
	SOCKS        SOCKSAccess `json:"socks"`
	LeaseSeconds int         `json:"lease_seconds"`
}

// SessionResponse is the opened session. Everything [Admin] answers about
// itself at open time travels here, so the controller never asks again.
type SessionResponse struct {
	SessionID      string    `json:"session_id"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	Account        string    `json:"account"`
	Accepted       Candidate `json:"accepted"`
	Transcript     []Step    `json:"transcript,omitempty"`
}

// SessionlessRequest reaches a device WITHOUT opening an administrative
// session: POST /mdp/v1/identify and /mdp/v1/onboard. The SOCKS credential is
// one-shot and short-lived; candidates follow the same order rule.
type SessionlessRequest struct {
	Connector  string      `json:"connector"`
	Target     TargetSpec  `json:"target"`
	Candidates []Candidate `json:"candidates,omitempty"`
	SOCKS      SOCKSAccess `json:"socks"`
}

// --- The composite read ---

// Read part names: the nine reads readDeviceState used to spend nine round
// trips on, collapsed into one wire call executed sequentially on the pinned
// session (pager-safe — two concurrent reads on one CLI session would
// interleave two pagers).
const (
	PartState        = "state"
	PartIdentity     = "identity"
	PartFaceplate    = "faceplate"
	PartMgmtAddr     = "mgmt-addr"
	PartSpanningTree = "spanning-tree"
	PartPoE          = "poe"
	PartOptics       = "optics"
	PartLLDP         = "lldp"
	PartFDB          = "fdb"
)

// ReadRequest asks for several parts on one session:
// POST /mdp/v1/sessions/{id}/read.
type ReadRequest struct {
	Parts []string `json:"parts"`
}

// ReadPart is one part's outcome: a result OR an error, never both. A part
// the platform lacks is an ordinary [ErrorBody] here — one missing read must
// not cost the eight that succeeded, which is the entire reason the composite
// exists.
type ReadPart struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *ErrorBody      `json:"error,omitempty"`
}

// ReadResponse maps part name → outcome for the parts that were REQUESTED.
// An absent key is a request bug, not a platform fact.
type ReadResponse struct {
	Parts map[string]ReadPart `json:"parts"`
}

// --- Op payloads ---
//
// Ops whose in-process form takes scalars get a named wire shape here; ops
// that already take a contract struct (PortPlan, DHCPSnoopPlan, …) send that
// struct as the body verbatim — one shape, both worlds.

// SetSecretRequest: POST sessions/{id}/set-secret.
type SetSecretRequest struct {
	OldSecret string `json:"old_secret"`
	NewSecret string `json:"new_secret"`
}

// HostnameRequest: POST sessions/{id}/hostname.
type HostnameRequest struct {
	Name string `json:"name"`
}

// MgmtVLANRequest: the mgmt-prepare / mgmt-addressed / mgmt-commit trio.
type MgmtVLANRequest struct {
	VLANID int `json:"vlan_id"`
}

// MgmtAddressedResponse: what MgmtAddressed answered.
type MgmtAddressedResponse struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// MgmtJumpRequest: POST sessions/{id}/mgmt-jump.
type MgmtJumpRequest struct {
	VLANID int            `json:"vlan_id"`
	Want   MgmtAddrIntent `json:"want"`
}

// MgmtJumpResponse carries the pre-write backup, the only way back on the
// platform this gesture exists for.
type MgmtJumpResponse struct {
	Backup []byte `json:"backup,omitempty"`
}

// StaticOwnerProbeRequest: POST sessions/{id}/mgmt-static-owner-probe.
type StaticOwnerProbeRequest struct {
	VLANID int    `json:"vlan_id"`
	Addr   string `json:"addr"`
	Mask   string `json:"mask"`
}

// StaticOwnerProbeResponse mirrors MgmtStaticOwnerProber's answer.
type StaticOwnerProbeResponse struct {
	Owner int    `json:"owner"`
	Raw   string `json:"raw,omitempty"`
}

// RawReadRequest: POST sessions/{id}/raw-read.
type RawReadRequest struct {
	Unit string `json:"unit"`
}

// RawReadResponse names what "a unit" means on this platform alongside the
// content, exactly as RawKind does in process.
type RawReadResponse struct {
	Content string `json:"content"`
	Kind    string `json:"kind"`
}

// AccountRequest: ensure-account (user+secret) and delete-account (user).
type AccountRequest struct {
	User   string `json:"user"`
	Secret string `json:"secret,omitempty"`
}

// LAGsRequest: POST sessions/{id}/lags.
type LAGsRequest struct {
	Desired []LAGState `json:"desired"`
}

// PortNameRequest / PortNameResponse: GET-shaped standard-port-name, POSTed
// as a body for symmetry with every other op.
type PortNameRequest struct {
	Connector  string `json:"connector"`
	DeviceName string `json:"device_name"`
}

type PortNameResponse struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
}

// WebUIResponse: GET /mdp/v1/webui?connector=&host=.
type WebUIResponse struct {
	Base string  `json:"base"`
	Auth WebAuth `json:"auth"`
}

// BulkReader is the composite read in Go form: the interface a controller-side
// adapter implements over POST sessions/{id}/read, and the one the caller
// prefers over nine sequential single reads when it is present. In-tree
// drivers never implement it — nine calls in one process cost nothing.
type BulkReader interface {
	ReadAll(ctx context.Context, parts []string) (map[string]ReadPart, error)
}

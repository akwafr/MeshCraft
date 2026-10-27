package restdriver

import (
	"context"
	"errors"
	"sync"
	"time"

	"meshcraft/wire/mdpv1"
)

// Admin is one wire session, satisfying [mdpv1.Admin] AND every optional
// capability interface. Assertions on it always succeed — the supports list
// cached on the connector is what answers honestly: a gesture the driver
// never declared returns [mdpv1.ErrNotImplemented] locally, no wire call, no
// session spent (the same shape the in-process refusal path has always had).
type Admin struct {
	conn     *Connector
	id       string
	minted   string
	account  string
	accepted mdpv1.Candidate

	mu         sync.Mutex
	transcript []mdpv1.Step
	closed     bool
}

func (a *Admin) Connector() mdpv1.Info     { return a.conn.info }
func (a *Admin) Account() string           { return a.account }
func (a *Admin) Accepted() mdpv1.Candidate { return a.accepted }

// Transcript fetches the live record — it GROWS on the driver side with
// every op. A transport failure answers the last copy this side saw: a
// transcript that stops early beats one that vanishes.
func (a *Admin) Transcript() []mdpv1.Step {
	a.mu.Lock()
	var id, closed = a.id, a.closed
	a.mu.Unlock()
	if id == "" || closed {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.transcript
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out struct {
		Transcript []mdpv1.Step `json:"transcript"`
	}
	var err = a.conn.client.get(ctx, "/sessions/"+id+"/transcript", nil, &out)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		a.transcript = out.Transcript
	}
	return a.transcript
}

// Close ends the wire session and revokes the SOCKS credential — the two
// halves of "give the device its slot back". Idempotent, like the contract
// demands; a wire failure still revokes, and the driver's reaper finishes
// the job when the lease runs out.
func (a *Admin) Close() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	var id = a.id
	a.mu.Unlock()
	if id != "" {
		var ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = a.conn.client.delete(ctx, "/sessions/"+id)
	}
	if a.minted != "" {
		a.conn.client.rt.Minter.Revoke(a.minted)
	}
}

// op posts one session op.
func (a *Admin) op(ctx context.Context, name string, in, out any) error {
	a.mu.Lock()
	var id, closed = a.id, a.closed
	a.mu.Unlock()
	if id == "" || closed {
		return errors.New("this access has no live session")
	}
	return a.conn.client.post(ctx, "/sessions/"+id+"/"+name, in, out, a.conn.name)
}

// gate refuses locally what the driver never declared.
func (a *Admin) gate(g mdpv1.Gesture) error {
	if !a.conn.has(g) {
		return mdpv1.ErrNotImplemented
	}
	return nil
}

// --- Mandatory surface ---

func (a *Admin) ReadState(ctx context.Context) (mdpv1.State, error) {
	var out mdpv1.State
	var err = a.op(ctx, "state", nil, &out)
	return out, err
}

func (a *Admin) ApplyPorts(ctx context.Context, plan mdpv1.PortPlan) (mdpv1.ApplyResult, error) {
	var out mdpv1.ApplyResult
	var err = a.op(ctx, "apply-ports", plan, &out)
	return out, err
}

func (a *Admin) SetSecret(ctx context.Context, oldSecret, newSecret string) error {
	return a.op(ctx, "set-secret",
		mdpv1.SetSecretRequest{OldSecret: oldSecret, NewSecret: newSecret}, nil)
}

// ReadAll is [mdpv1.BulkReader]: the nine reads in one wire call.
func (a *Admin) ReadAll(ctx context.Context, parts []string) (map[string]mdpv1.ReadPart, error) {
	var out mdpv1.ReadResponse
	var err = a.op(ctx, "read", mdpv1.ReadRequest{Parts: parts}, &out)
	return out.Parts, err
}

// --- Optional interfaces, one block per gesture ---

func (a *Admin) Accounts(ctx context.Context) ([]mdpv1.Account, error) {
	if a.gate(mdpv1.GestureAccounts) != nil {
		return nil, mdpv1.ErrNotImplemented
	}
	var out []mdpv1.Account
	var err = a.op(ctx, "accounts", nil, &out)
	return out, err
}

func (a *Admin) EnsureAccount(ctx context.Context, user, secret string) error {
	if a.gate(mdpv1.GestureAccounts) != nil {
		return mdpv1.ErrNotImplemented
	}
	return a.op(ctx, "ensure-account", mdpv1.AccountRequest{User: user, Secret: secret}, nil)
}

func (a *Admin) DeleteAccount(ctx context.Context, user string) error {
	if a.gate(mdpv1.GestureAccounts) != nil {
		return mdpv1.ErrNotImplemented
	}
	return a.op(ctx, "delete-account", mdpv1.AccountRequest{User: user}, nil)
}

func (a *Admin) Persist(ctx context.Context) error {
	if a.gate(mdpv1.GesturePersist) != nil {
		return mdpv1.ErrNotImplemented
	}
	return a.op(ctx, "persist", nil, nil)
}

func (a *Admin) ApplySNMP(ctx context.Context, want mdpv1.SNMPIdentity) (mdpv1.SNMPResult, error) {
	var out mdpv1.SNMPResult
	if a.gate(mdpv1.GestureSNMPConfig) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "snmp-config", want, &out)
	return out, err
}

func (a *Admin) ApplyTrapReceiver(ctx context.Context,
	want mdpv1.TrapDestination) (mdpv1.TrapResult, error) {
	var out mdpv1.TrapResult
	if a.gate(mdpv1.GestureTrapReceiver) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "trap-receiver", want, &out)
	return out, err
}

func (a *Admin) PrepareMgmt(ctx context.Context, vlanID int) error {
	if a.gate(mdpv1.GestureMgmtMove) != nil {
		return mdpv1.ErrNotImplemented
	}
	return a.op(ctx, "mgmt-prepare", mdpv1.MgmtVLANRequest{VLANID: vlanID}, nil)
}

func (a *Admin) MgmtAddressed(ctx context.Context, vlanID int) (bool, string, error) {
	if a.gate(mdpv1.GestureMgmtMove) != nil {
		return false, "", mdpv1.ErrNotImplemented
	}
	var out mdpv1.MgmtAddressedResponse
	var err = a.op(ctx, "mgmt-addressed", mdpv1.MgmtVLANRequest{VLANID: vlanID}, &out)
	return out.OK, out.Detail, err
}

func (a *Admin) CommitMgmt(ctx context.Context, vlanID int) error {
	if a.gate(mdpv1.GestureMgmtMove) != nil {
		return mdpv1.ErrNotImplemented
	}
	return a.op(ctx, "mgmt-commit", mdpv1.MgmtVLANRequest{VLANID: vlanID}, nil)
}

func (a *Admin) JumpMgmt(ctx context.Context, vlanID int,
	want mdpv1.MgmtAddrIntent) ([]byte, error) {
	if a.gate(mdpv1.GestureMgmtJump) != nil {
		return nil, mdpv1.ErrNotImplemented
	}
	var out mdpv1.MgmtJumpResponse
	var err = a.op(ctx, "mgmt-jump", mdpv1.MgmtJumpRequest{VLANID: vlanID, Want: want}, &out)
	return out.Backup, err
}

func (a *Admin) ProbeStaticOwner(ctx context.Context, vlanID int,
	addr, mask string) (int, string, error) {
	if a.gate(mdpv1.GestureMgmtStaticOwnerProbe) != nil {
		return 0, "", mdpv1.ErrNotImplemented
	}
	var out mdpv1.StaticOwnerProbeResponse
	var err = a.op(ctx, "mgmt-static-owner-probe",
		mdpv1.StaticOwnerProbeRequest{VLANID: vlanID, Addr: addr, Mask: mask}, &out)
	return out.Owner, out.Raw, err
}

func (a *Admin) ReadRaw(ctx context.Context, unit string) (string, error) {
	if a.gate(mdpv1.GestureRawRead) != nil {
		return "", mdpv1.ErrNotImplemented
	}
	var out mdpv1.RawReadResponse
	var err = a.op(ctx, "raw-read", mdpv1.RawReadRequest{Unit: unit}, &out)
	return out.Content, err
}

// RawKind has no context in its contract signature: a short wire budget, and
// a transport failure names the generic thing.
func (a *Admin) RawKind() string {
	if a.gate(mdpv1.GestureRawRead) != nil {
		return ""
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out mdpv1.RawReadResponse
	if a.op(ctx, "raw-read", mdpv1.RawReadRequest{}, &out) != nil {
		return "configuration unit"
	}
	return out.Kind
}

func (a *Admin) ReadFaceplate(ctx context.Context) ([]map[string]any, error) {
	if a.gate(mdpv1.GestureFaceplate) != nil {
		return nil, mdpv1.ErrNotImplemented
	}
	var out []map[string]any
	var err = a.op(ctx, "faceplate", nil, &out)
	return out, err
}

func (a *Admin) ReadIdentity(ctx context.Context) (mdpv1.Identity, error) {
	var out mdpv1.Identity
	if a.gate(mdpv1.GestureIdentity) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "identity", nil, &out)
	return out, err
}

func (a *Admin) ReadDHCPSnooping(ctx context.Context) (mdpv1.DHCPSnoopState, error) {
	var out mdpv1.DHCPSnoopState
	if a.gate(mdpv1.GestureDHCPSnoopRead) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "dhcp-snooping-read", nil, &out)
	return out, err
}

func (a *Admin) ApplyDHCPSnooping(ctx context.Context,
	plan mdpv1.DHCPSnoopPlan) (mdpv1.ApplyResult, error) {
	var out mdpv1.ApplyResult
	if a.gate(mdpv1.GestureDHCPSnoopApply) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "dhcp-snooping-apply", plan, &out)
	return out, err
}

func (a *Admin) ReadStormControl(ctx context.Context) (mdpv1.StormControlState, error) {
	var out mdpv1.StormControlState
	if a.gate(mdpv1.GestureStormControlRead) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "storm-control-read", nil, &out)
	return out, err
}

func (a *Admin) ApplyStormControl(ctx context.Context,
	plan mdpv1.StormControlPlan) (mdpv1.ApplyResult, error) {
	var out mdpv1.ApplyResult
	if a.gate(mdpv1.GestureStormControlApply) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "storm-control-apply", plan, &out)
	return out, err
}

func (a *Admin) ReadSpanningTree(ctx context.Context) (mdpv1.SpanningTreeState, error) {
	var out mdpv1.SpanningTreeState
	if a.gate(mdpv1.GestureSpanningTreeRead) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "spanning-tree-read", nil, &out)
	return out, err
}

func (a *Admin) ApplySpanningTree(ctx context.Context,
	plan mdpv1.SpanningTreePlan) (mdpv1.ApplyResult, error) {
	var out mdpv1.ApplyResult
	if a.gate(mdpv1.GestureSpanningTreeApply) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "spanning-tree-apply", plan, &out)
	return out, err
}

func (a *Admin) ReadPoE(ctx context.Context) (mdpv1.PoEState, error) {
	var out mdpv1.PoEState
	if a.gate(mdpv1.GesturePoERead) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "poe-read", nil, &out)
	return out, err
}

func (a *Admin) ApplyPoE(ctx context.Context, plan mdpv1.PoEPlan) (mdpv1.ApplyResult, error) {
	var out mdpv1.ApplyResult
	if a.gate(mdpv1.GesturePoEApply) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "poe-apply", plan, &out)
	return out, err
}

func (a *Admin) ApplyPortAdmin(ctx context.Context,
	plan mdpv1.PortAdminPlan) (mdpv1.ApplyResult, error) {
	var out mdpv1.ApplyResult
	if a.gate(mdpv1.GesturePortAdmin) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "port-admin", plan, &out)
	return out, err
}

func (a *Admin) CyclePortPower(ctx context.Context,
	plan mdpv1.PortCyclePlan) (mdpv1.ApplyResult, error) {
	var out mdpv1.ApplyResult
	if a.gate(mdpv1.GesturePortPowerCycle) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "port-power-cycle", plan, &out)
	return out, err
}

func (a *Admin) ApplyLAGs(ctx context.Context,
	desired []mdpv1.LAGState) (mdpv1.ApplyResult, error) {
	var out mdpv1.ApplyResult
	if a.gate(mdpv1.GestureLAGs) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "lags", mdpv1.LAGsRequest{Desired: desired}, &out)
	return out, err
}

func (a *Admin) SetHostname(ctx context.Context, name string) error {
	if a.gate(mdpv1.GestureHostname) != nil {
		return mdpv1.ErrNotImplemented
	}
	return a.op(ctx, "hostname", mdpv1.HostnameRequest{Name: name}, nil)
}

func (a *Admin) ReadLLDPObservation(ctx context.Context) (mdpv1.LLDPObservation, error) {
	var out mdpv1.LLDPObservation
	if a.gate(mdpv1.GestureLLDP) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "lldp", nil, &out)
	return out, err
}

func (a *Admin) ReadFDBObservation(ctx context.Context) (mdpv1.FDBObservation, error) {
	var out mdpv1.FDBObservation
	if a.gate(mdpv1.GestureFDB) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "fdb", nil, &out)
	return out, err
}

func (a *Admin) ReadOptics(ctx context.Context) (mdpv1.OpticsState, error) {
	var out mdpv1.OpticsState
	if a.gate(mdpv1.GestureOptics) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "optics", nil, &out)
	return out, err
}

func (a *Admin) ReadMgmtAddressing(ctx context.Context) (mdpv1.MgmtAddrState, error) {
	var out mdpv1.MgmtAddrState
	if a.gate(mdpv1.GestureMgmtAddrRead) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "mgmt-addr-read", nil, &out)
	return out, err
}

func (a *Admin) ApplyMgmtAddressing(ctx context.Context,
	want mdpv1.MgmtAddrIntent) (mdpv1.MgmtAddrResult, error) {
	var out mdpv1.MgmtAddrResult
	if a.gate(mdpv1.GestureMgmtAddrApply) != nil {
		return out, mdpv1.ErrNotImplemented
	}
	var err = a.op(ctx, "mgmt-addr-apply", want, &out)
	return out, err
}

// Compile-time: the adapter satisfies the mandatory contract, the bulk read,
// and every session-scoped optional interface. The connector-level ones are
// checked in restdriver.go's Connector.
var (
	_ mdpv1.Admin                 = (*Admin)(nil)
	_ mdpv1.BulkReader            = (*Admin)(nil)
	_ mdpv1.AccountManager        = (*Admin)(nil)
	_ mdpv1.Persister             = (*Admin)(nil)
	_ mdpv1.SNMPConfigurator      = (*Admin)(nil)
	_ mdpv1.TrapPusher            = (*Admin)(nil)
	_ mdpv1.MgmtMover             = (*Admin)(nil)
	_ mdpv1.MgmtJumper            = (*Admin)(nil)
	_ mdpv1.MgmtStaticOwnerProber = (*Admin)(nil)
	_ mdpv1.RawReader             = (*Admin)(nil)
	_ mdpv1.FaceplateReader       = (*Admin)(nil)
	_ mdpv1.IdentityReader        = (*Admin)(nil)
	_ mdpv1.DHCPSnoopReader       = (*Admin)(nil)
	_ mdpv1.DHCPSnoopApplier      = (*Admin)(nil)
	_ mdpv1.StormControlReader    = (*Admin)(nil)
	_ mdpv1.StormControlApplier   = (*Admin)(nil)
	_ mdpv1.SpanningTreeReader    = (*Admin)(nil)
	_ mdpv1.SpanningTreeApplier   = (*Admin)(nil)
	_ mdpv1.PoEReader             = (*Admin)(nil)
	_ mdpv1.PoEApplier            = (*Admin)(nil)
	_ mdpv1.PortAdminApplier      = (*Admin)(nil)
	_ mdpv1.PortPowerCycler       = (*Admin)(nil)
	_ mdpv1.LAGWriter             = (*Admin)(nil)
	_ mdpv1.HostnameSetter        = (*Admin)(nil)
	_ mdpv1.LLDPReader            = (*Admin)(nil)
	_ mdpv1.FDBReader             = (*Admin)(nil)
	_ mdpv1.OpticsReader          = (*Admin)(nil)
	_ mdpv1.MgmtAddrReader        = (*Admin)(nil)
	_ mdpv1.MgmtAddrWriter        = (*Admin)(nil)
)

package api

import (
	"context"
	"encoding/json"

	"meshcraft/internal/driver"
	"meshcraft/wire/mdpv1"
)

// The composite-read fast path for readDeviceState.
//
// In process, the nine reads of the state gesture cost nine method calls —
// nothing. Over the MDP wire each would be an HTTP round trip on the pinned
// session, so an external driver implements [mdpv1.BulkReader] and the whole
// gesture becomes ONE call, executed sequentially driver-side (the pager
// rule). This wrapper makes that transparent to readDeviceState's helpers:
// it prefetches every part, then serves the ordinary reader interfaces from
// the prefetch — the helpers keep asserting and calling exactly as they do
// on an in-tree Admin.
//
// A part the driver answered with not_implemented serves
// [driver.ErrNotImplemented] from the corresponding method; the helpers
// translate that into the same "cannot read X on this product line" sentence
// an in-process failed assertion produces, so the two worlds read identically
// on screen.

// prefetchIfBulk returns adm unchanged for in-tree drivers, or a
// prefetch-backed view when the admin can bulk-read. A failed prefetch falls
// back to the direct admin: slower, never worse.
func prefetchIfBulk(ctx context.Context, adm driver.Admin) driver.Admin {
	var bulk, ok = adm.(mdpv1.BulkReader)
	if !ok {
		return adm
	}
	var parts, err = bulk.ReadAll(ctx, []string{
		mdpv1.PartState, mdpv1.PartIdentity, mdpv1.PartFaceplate,
		mdpv1.PartMgmtAddr, mdpv1.PartSpanningTree, mdpv1.PartPoE,
		mdpv1.PartOptics, mdpv1.PartLLDP, mdpv1.PartFDB,
	})
	if err != nil {
		return adm
	}
	return &prefetchedAdmin{Admin: adm, parts: parts}
}

// prefetchedAdmin serves the nine read interfaces from a completed composite
// read. Everything else — writes, transcript, close — passes through to the
// real session.
type prefetchedAdmin struct {
	driver.Admin
	parts map[string]mdpv1.ReadPart
}

// part decodes one prefetched part into out.
func (p *prefetchedAdmin) part(name string, out any) error {
	var got, ok = p.parts[name]
	if !ok {
		return driver.ErrNotImplemented
	}
	if got.Error != nil {
		return got.Error.ToError(p.Admin.Connector().Name)
	}
	return json.Unmarshal(got.Result, out)
}

func (p *prefetchedAdmin) ReadState(_ context.Context) (driver.State, error) {
	var out driver.State
	var err = p.part(mdpv1.PartState, &out)
	return out, err
}

func (p *prefetchedAdmin) ReadIdentity(_ context.Context) (driver.Identity, error) {
	var out driver.Identity
	var err = p.part(mdpv1.PartIdentity, &out)
	return out, err
}

func (p *prefetchedAdmin) ReadFaceplate(_ context.Context) ([]map[string]any, error) {
	var out []map[string]any
	var err = p.part(mdpv1.PartFaceplate, &out)
	return out, err
}

func (p *prefetchedAdmin) ReadMgmtAddressing(_ context.Context) (driver.MgmtAddrState, error) {
	var out driver.MgmtAddrState
	var err = p.part(mdpv1.PartMgmtAddr, &out)
	return out, err
}

func (p *prefetchedAdmin) ReadSpanningTree(_ context.Context) (driver.SpanningTreeState, error) {
	var out driver.SpanningTreeState
	var err = p.part(mdpv1.PartSpanningTree, &out)
	return out, err
}

func (p *prefetchedAdmin) ReadPoE(_ context.Context) (driver.PoEState, error) {
	var out driver.PoEState
	var err = p.part(mdpv1.PartPoE, &out)
	return out, err
}

func (p *prefetchedAdmin) ReadOptics(_ context.Context) (driver.OpticsState, error) {
	var out driver.OpticsState
	var err = p.part(mdpv1.PartOptics, &out)
	return out, err
}

func (p *prefetchedAdmin) ReadLLDPObservation(_ context.Context) (driver.LLDPObservation, error) {
	var out driver.LLDPObservation
	var err = p.part(mdpv1.PartLLDP, &out)
	return out, err
}

func (p *prefetchedAdmin) ReadFDBObservation(_ context.Context) (driver.FDBObservation, error) {
	var out driver.FDBObservation
	var err = p.part(mdpv1.PartFDB, &out)
	return out, err
}

package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"meshcraft/internal/auth"
	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/recon"
)

// AUTOMATIC application of configuration to the device.
//
// OPERATIONAL DECISION that departs from the spec. The pipeline in
// docs/01-Architecture/Reconciliation places explicit validation between plan and
// apply. Operations decided otherwise, for a simple reason: configuring forty-eight
// ports in the UI IS the approval. Leaving configuration pending for a second gesture,
// on a product whose promise is to administer the fleet, means asking twice for the
// same thing — and leaving a fleet permanently different from what the screen shows.
//
// What that does NOT allow:
//
//   - write guardrails stay intact, and they live IN THE CONNECTOR (cf. driver.Admin.ApplyPorts):
//     management VLAN is never rewritten, nor the native VLAN of a port carrying administration.
//     The rule really differs by platform — on Instant On we protect the native VLAN of an
//     active port, on SwOS it is TAGGING the management VLAN on that port that must not change
//     — and that is precisely why it is not here: the API layer cannot guarantee it for three
//     platforms it does not know;
//   - a failure is NEVER retried in a loop. One attempt per change, one event if it fails, and
//     the device stays "in progress" — visible, and re-appliable by hand. Retrying forever on a
//     refusing device is the best way to lock it out;
//   - one apply at a time per device (section 5, "serialization per device").

// applyDebounce gives a burst of changes time to finish.
//
// Configuring one port triggers one per port: without batching, changing forty-eight ports
// would launch forty-eight concurrent applies on a device that accepts only five sessions.
// The delay is short so apply still feels immediate.
const applyDebounce = 6 * time.Second

// autoApplier batches apply requests and serializes them per device.
type autoApplier struct {
	mu      sync.Mutex
	pending map[string]*time.Timer
	busy    map[string]bool
}

// NewAutoApplier builds the apply queue. Exported: controller mount point, and not mounting
// it is enough to revert to manual apply.
func NewAutoApplier() *autoApplier {
	return &autoApplier{pending: map[string]*time.Timer{}, busy: map[string]bool{}}
}

// schedule requests an apply for this device, after batching.
func (s *Server) schedule(deviceID string) {
	if s.Apply == nil {
		return
	}
	s.Apply.mu.Lock()
	defer s.Apply.mu.Unlock()
	// A burst in progress pushes the deadline back: we apply once, at the end.
	var t, seen = s.Apply.pending[deviceID]
	if seen {
		t.Reset(applyDebounce)
		return
	}
	s.Apply.pending[deviceID] = time.AfterFunc(applyDebounce, func() {
		s.Apply.mu.Lock()
		delete(s.Apply.pending, deviceID)
		if s.Apply.busy[deviceID] {
			// An apply is already running: relaunching now would work on state it is changing.
			// The change will be picked up on the next change, or manually.
			s.Apply.mu.Unlock()
			return
		}
		s.Apply.busy[deviceID] = true
		s.Apply.mu.Unlock()

		defer func() {
			s.Apply.mu.Lock()
			delete(s.Apply.busy, deviceID)
			s.Apply.mu.Unlock()
		}()

		// Fresh context: the HTTP request that triggered the change finished long ago.
		var ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		s.autoApplyDevice(ctx, deviceID)
	})
}

// autoApplyDevice applies configuration, with no human in the loop.
//
// Silent when all goes well: the result reads on the device badge, which turns green again.
// Loud when it fails — that is the only case someone must know.
func (s *Server) autoApplyDevice(ctx context.Context, deviceID string) {
	var d, err = s.Store.GetDevice(ctx, deviceID)
	if err != nil {
		return
	}
	var res applyOutcome
	res, err = s.applyDeviceConfig(ctx, d, false)
	if err != nil {
		// Trace is already taken by applyDeviceConfig, for both paths.
		slog.Warn("automatic apply failed", "device", d.Name, "err", err)
		s.emitDeviceBG(d, "config.apply_failed", model.SevWarning,
			"Automatic apply failed on \""+d.Name+"\": "+err.Error()+
				" — configuration stays pending, it can be re-applied manually.")
		return
	}
	if len(res.Remaining) > 0 || len(res.Result.Skipped) > 0 {
		s.emitDeviceBG(d, "config.applied", model.SevNotice,
			"Configuration applied on \""+d.Name+"\": "+itoa(len(res.Result.PortsWritten))+
				" port(s), "+itoa(len(res.Result.VLANsCreated))+" VLAN(s) created, "+
				itoa(len(res.Result.Skipped))+" skipped, "+itoa(len(res.Remaining))+
				" drift(s) remaining.")
		return
	}
	s.emitDeviceBG(d, "config.applied", model.SevInfo,
		"Configuration applied and verified on \""+d.Name+"\": "+
			itoa(len(res.Result.PortsWritten))+" port(s), "+
			itoa(len(res.Result.VLANsCreated))+" VLAN(s) created.")
}

// emitDeviceBG logs outside an HTTP request: there is no user to attribute the gesture to,
// and that is information in itself.
func (s *Server) emitDeviceBG(d model.Device, evType string, sev int, msg string) {
	var siteID, deviceID = d.SiteID, d.ID
	_ = s.Store.InsertEvent(context.Background(), model.Event{
		SiteID: &siteID, DeviceID: &deviceID,
		Type: evType, Severity: sev, Message: msg, Actor: "meshcraft",
	})
	// Automatic apply finishes LONG after the request that triggered it: without this
	// notification, the screen would stay on "pending" although sync state turned green again.
	s.NotifySiteChanged(siteID, ChangeSync)
}

// mustReload re-reads the device, or returns what we had: the decision that follows is a
// display choice, not a write — a failed read must not fail an apply that succeeded.
func mustReload(ctx context.Context, s *Server, deviceID string) model.Device {
	var d, err = s.Store.GetDevice(ctx, deviceID)
	if err != nil {
		return model.Device{}
	}
	return d
}

// applyOutcome is what an apply produced.
type applyOutcome struct {
	Result    driver.ApplyResult
	Remaining []recon.Change
	Steps     []driver.Step
	Sync      recon.Sync
	// Unsupported: intents the PLATFORM cannot write, stated plainly. Neither an error nor a
	// security refusal like ApplyResult.Skipped — a connector limit, and it does not prevent
	// considering the device up to date for everything else.
	Unsupported []string
	// Notes: what the connector understood about the device when RE-READING after apply, and
	// what [driver.State] cannot say otherwise.
	//
	// THE FIELD THAT AVOIDS ANNOUNCING SEGMENTATION THAT DOES NOT EXIST, born from a fact
	// observed on the two new platforms: VLAN configuration can be PRESENT and INERT. On SwOS,
	// a port whose VLAN mode is "disabled" filters nothing — its PVID and membership are
	// configured but have no effect on traffic. On RouterOS, a bridge with inactive
	// `vlan-filtering` lets everything pass everywhere.
	//
	// An apply can therefore succeed technically, verified by re-read, without any effect.
	// These are PHRASES for an operator, not codes: we display them, draw no decision from them
	// — a connector that wants a decision made must refuse itself.
	Notes []string
}

// unsupportedNotes says what apply did NOT write because this connector cannot write it — not
// because it refused.
//
// Distinction from `Skipped` is the heart of the matter: a skipped port is a security decision
// the operator can override by forcing; an unsupported field cannot be forced, it must be
// implemented. Mixing them would offer a "force" button that changes nothing.
//
// These intents do NOT count as drift: they are not in recon.PortIntent, therefore never
// diffed, therefore the device is dated as applied once VLAN membership is in place. That is
// intended behavior — "published for everything supported" — but it only becomes honest once
// we ANNOUNCE it, which this function does.
//
// THE CONNECTOR IS A PARAMETER, and it was just resolved by discovery: the answer depends on
// platform, and reading it on the Instant On connector for a MikroTik announced one line's
// capabilities on another. [driver.Connector] not [driver.Admin]: the answer depends neither on
// a session nor a model, and the caller needs it BEFORE reaching the device.
func unsupportedNotes(conn driver.Connector, d model.Device) []string {
	var out []string
	var key string
	for _, key = range recon.ExtraIntents {
		if conn.WritesExtra(key) {
			continue
		}
		var ports = recon.RequestedExtras(d.Ports)[key]
		if len(ports) == 0 {
			continue
		}
		out = append(out, recon.ExtraLabel(key)+" : requested on "+itoa(len(ports))+
			" port(s) ("+portsLabel(ports)+") — this connector does not apply this intent yet. "+
			"The rest of the configuration is in place.")
	}
	return out
}

// applyDeviceConfig applies configuration and RECORDS the result, success or failure.
//
// Failure trace is taken here, not at callers: it used to be only in automatic apply, so a
// failure from the "re-apply" button left nothing in `last_apply`. The screen could therefore
// not offer replay — half of failures were invisible exactly where the operator looks.
func (s *Server) applyDeviceConfig(ctx context.Context, d model.Device,
	force bool) (applyOutcome, error) {
	var out, err = s.runApply(ctx, d, force)
	if err != nil {
		s.rememberApply(ctx, d.ID, out, err)
		s.recordPipeline(ctx, pipelineForApply(d, out, err), auth.UserFrom(ctx).ID)
	}
	return out, err
}

// runApply is the HEART of apply, shared by the "force" button and automatic apply. Two
// distinct paths would eventually diverge, and the day they diverge one of them applies
// something other than what it announces.
func (s *Server) runApply(ctx context.Context, d model.Device,
	force bool) (applyOutcome, error) {
	var out applyOutcome
	if d.MgmtIP == "" {
		return out, errNoMgmtIP
	}
	// CONNECTOR FIRST, BEFORE TRANSPORT, and the order is not cosmetic: what the platform
	// cannot write does not depend on connection success, and the operator needs it as much
	// when apply fails. Resolving here rather than after `dialerFor` means the screen shows
	// "PoE is not applicable on this line" even on an unreachable device — i.e. when looking
	// for why nothing is in place.
	var conn, connErr = s.connectorFor(ctx, d)
	if connErr != nil {
		return out, connErr
	}
	out.Unsupported = unsupportedNotes(conn, d)

	var plan, planErr = s.Store.GetMgmtPlan(ctx, d.SiteID)
	if planErr != nil {
		return out, planErr
	}
	// Site VLAN plan accompanies apply: an adopted device must know its site's VLANs, not
	// only those its ports use.
	var siteVlans, vlanErr = s.Store.ListVlans(ctx, d.SiteID)
	if vlanErr != nil {
		return out, vlanErr
	}
	var vlans []driver.VLAN
	var vlanIDs []int
	var v model.Vlan
	for _, v = range siteVlans {
		vlans = append(vlans, driver.VLAN{ID: v.VlanID, Name: v.Name})
		vlanIDs = append(vlanIDs, v.VlanID)
	}

	// "all" resolves to enumerating the site's VLANs, here as in the diff (cf. recon.Resolve).
	// Before, those ports were dropped silently: a trunk configured in the UI never reached the
	// device, and nothing said so — no banner, no drift, no event.
	var desired = map[string]driver.PortState{}
	var name string
	var p recon.PortIntent
	for name, p = range recon.Resolve(recon.ParsePorts(d.Ports), vlanIDs) {
		desired[name] = driver.PortState{
			Mode: p.Mode, PVID: p.PVID, Tagged: p.Tagged,
			// Resolved "all" is an exact site VLAN set (add and remove). TaggedAtLeast
			// only while "all" is unresolved — empty site plan must not strip trunks.
			TaggedAtLeast: p.TaggedAll && len(p.Tagged) == 0,
		}
	}

	var _, adm, _, err = s.adminFor(ctx, d)
	out.Steps = transcriptOf(adm)
	if err != nil {
		return out, err
	}
	// ONE AGGREGATE NOT FIVE PARAMETERS: the day apply needs one more constraint, all three
	// connectors change signature together or not at all — a parameter added in three places
	// gets forgotten on the third, and that platform's apply would write something other than
	// what the screen announces.
	//
	// APPLY PERSISTS BY ITSELF when the platform distinguishes the two: the contract decides,
	// not us. Chaining [driver.Persister] here would be one more round trip on an interface
	// that accepts only five sessions.
	out.Result, err = adm.ApplyPorts(ctx, driver.PortPlan{
		Desired: desired, MgmtVLAN: plan.VlanID, SiteVLANs: vlans, Force: force,
	})
	out.Steps = adm.Transcript()

	// Spanning-tree: site preference ∩ connector caps, then port intents.
	// Runs even when ApplyPorts failed: VLAN drift must not block edge / mode push.
	var stpRes, stpErr = s.applyDeviceSTP(ctx, d, conn, adm)
	out.Steps = adm.Transcript()
	if stpErr != nil {
		if err == nil {
			err = stpErr
		} else {
			out.Notes = append(out.Notes, "spanning tree apply also failed: "+stpErr.Error())
		}
	} else {
		out.Result.Skipped = append(out.Result.Skipped, stpRes.Skipped...)
		out.Result.Notes = append(out.Result.Notes, stpRes.Notes...)
		out.Notes = append(out.Notes, stpRes.Notes...)
	}
	if err != nil {
		return out, err
	}
	// Nothing is considered applied on return code alone: we RE-READ and compare (section 5,
	// Verify). That re-read is what authorizes dating the apply.
	var state driver.State
	if state, err = adm.ReadState(ctx); err != nil {
		out.Steps = adm.Transcript()
		return out, err
	}
	out.Steps = adm.Transcript()
	// Apply-time facts first (floors, etc.), then RE-READ notes: observed state afterward
	// says whether applied config has an effect. Both land in last_apply.notes — never in
	// Skipped, or the screen would offer Force for something Force cannot change.
	out.Notes = append(append([]string{}, out.Result.Notes...), state.Notes...)
	if err = s.saveObservedState(ctx, d.ID, state); err != nil {
		return out, err
	}
	// Date apply ONLY if it applied everything.
	//
	// A deliberately skipped port leaves drift that is NOT drift: nobody touched the device,
	// meshcraft refused. Dating apply would attribute that drift to the device and show "changed
	// outside meshcraft", which is false and sends people looking for a culprit that does not
	// exist. While drift remains, intent is simply ahead — and the skipped-ports banner says
	// why it stays that way.
	var settled = len(driftOf(mustReload(ctx, s, d.ID), vlanIDs)) == 0
	if settled {
		if err = s.Store.MarkApplied(ctx, d.ID); err != nil {
			return out, err
		}
	}
	var fresh model.Device
	if fresh, err = s.Store.GetDevice(ctx, d.ID); err != nil {
		return out, err
	}
	out.Remaining = driftOf(fresh, vlanIDs)
	out.Sync = recon.State(fresh.ObservedAt, fresh.DesiredChangedAt, fresh.AppliedAt,
		out.Remaining)
	s.rememberApply(ctx, d.ID, out, nil)
	s.recordPipeline(ctx, pipelineForApply(d, out, nil), auth.UserFrom(ctx).ID)
	return out, nil
}

// rememberApply attaches to the device what apply did — especially what it skipped, with reason.
// That is what the screen will show: without this trace, a protective guardrail looks like a bug.
//
// # `backup_bytes`: SwOS's only rollback, stated out loud
//
// [driver.ApplyResult.Backup] is the device configuration BEFORE write, as the platform returned
// it. It exists for one platform and one reason: SwOS applies and persists in one gesture, a
// power cut cancels nothing, and this copy is the ONLY rollback that exists. The connector
// takes it just before the first write; it is not reconstructed afterward.
//
// It was PRODUCED THEN DISCARDED. Recording its SIZE does not keep it — separate work — but
// closes the worst of two gaps: not knowing we had it. An operator reading "4,812 bytes of
// backup taken before write" knows a safety net existed and was not kept; without that line,
// meshcraft held the device's only recourse for the length of an HTTP request, and nobody would
// ever know.
//
// ZERO IS NOT FAILURE. On an `apply_then_save` platform (Instant On), the net is elsewhere —
// no persist — and there is no copy to take. On SwOS, an EMPTY backup is FACTORY configuration,
// a perfectly legitimate state for a new switch. The field says what was returned, it does not judge.
func (s *Server) rememberApply(ctx context.Context, deviceID string,
	out applyOutcome, failure error) {
	var rec = map[string]any{
		"at":            time.Now(),
		"ports_written": out.Result.PortsWritten,
		"vlans_created": out.Result.VLANsCreated,
		"skipped":       out.Result.Skipped,
		"unsupported":   out.Unsupported,
		"remaining":     len(out.Remaining),
		"backup_bytes":  len(out.Result.Backup),
		// Connector phrases on RE-READ state: present-but-inert config is a successful apply
		// that segments nothing, and the screen must say so where it says skipped ports.
		"notes": out.Notes,
	}
	if failure != nil {
		rec["error"] = failure.Error()
	}
	var raw, err = json.Marshal(rec)
	if err != nil {
		return
	}
	_ = s.Store.SaveApplyOutcome(ctx, deviceID, raw)
}

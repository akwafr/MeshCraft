package api

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/snmpprov"
	"meshcraft/internal/store"
	"meshcraft/internal/tunnel"
)

// Push SNMP configuration ON the device.
//
// Last step of section 5 of docs/03-Security/SNMP identity, and the only one that
// touches the fleet. `ensureSNMPIdentity` prepares the ground — tag and secret in the
// database — but a secret the device ignores supervises nothing: without this push, the
// vault describes supervision that does not exist.
//
// Done AT PROVISIONING, like everything else: adopting a device means making it
// administrable AND observable. Leaving supervision to a second manual gesture
// guaranteed a half-supervised fleet, each omission invisible.

// snmpIdentityToPose reads from the vault what must be pushed.
//
// Returns `ok` false silently when there is nothing to push: a device without an
// exploitable SNMP plan is not an anomaly, it is a device that cannot be supervised, and
// `snmpPlanFor` already said so with its reason.
func (s *Server) snmpIdentityToPose(ctx context.Context,
	d model.Device) (driver.SNMPIdentity, bool) {
	var st, err = s.snmpPlanFor(ctx, d)
	if err != nil || !st.Plan.Supervised() || st.CredentialID == "" {
		return driver.SNMPIdentity{}, false
	}
	// The v3 username comes from the vault: it is the one that was generated, and
	// pushing another name would make the secret unusable.
	var cred, enc, enc2, revErr = s.Store.RevealSiteCredential(ctx, st.CredentialID)
	if revErr != nil {
		return driver.SNMPIdentity{}, false
	}
	var clear string
	if clear, err = s.Secrets.Open(enc); err != nil {
		return driver.SNMPIdentity{}, false
	}
	// SOURCE BOUNDS accompany both halves: a platform with this setting refuses to write
	// without it (cf. snmpSourcesFor), a platform without it ignores it by SAYING SO in
	// its `Skipped`.
	var sources = s.snmpSourcesFor(ctx, d)

	switch st.Plan.Kind {
	case snmpprov.KindV2c:
		// The community IS the secret: there is no associated account.
		return driver.SNMPIdentity{Community: clear, Sources: sources}, true
	case snmpprov.KindV3:
		// TWO distinct passphrases are the whole point of the vault's second column
		// (migration 0017). Reusing the same on both sides fed exactly the same value into
		// both RFC 3414 derivations: the encryption key was then only the prefix of the
		// authentication key, and encryption added nothing.
		//
		// Without a second passphrase, we PUSH NOTHING. Pushing anyway by duplicating the
		// first would restore the defect, this time without even a comment to flag it; the
		// plan (snmpPlanFor) already carries the missing note.
		if len(enc2) == 0 {
			return driver.SNMPIdentity{}, false
		}
		var clear2 string
		if clear2, err = s.Secrets.Open(enc2); err != nil {
			return driver.SNMPIdentity{}, false
		}
		return driver.SNMPIdentity{
			User: cred.Username, AuthAlg: st.Plan.Auth, PrivAlg: st.Plan.Priv,
			AuthSecret: clear, PrivSecret: clear2, Sources: sources,
		}, true
	}
	return driver.SNMPIdentity{}, false
}

// snmpSourcesFor bounds addresses allowed to QUERY the device's SNMP agent.
//
// # Why this field exists, and why its absence is not "not supported"
//
// RouterOS REFUSES to write a community without bounding its sources: the `addresses`
// field of `/snmp/community` must be set, and the connector refuses to invent it —
// "leaving SNMP open to all addresses must be requested explicitly" (observation shows
// `::/0` on the reference device, therefore ALL sources, including outside the
// management VLAN).
//
// This refusal is NOT [driver.ErrNotImplemented], and it must especially not display as
// "this platform cannot do it": it is a MISSING PARAMETER, not a missing capability. The
// missing gesture was on the caller's side, and that is done here.
//
// # THE TWO SOURCES ARE CUMULATED, AND THAT IS A FIX
//
// This function used to return the plan subnet OR, only as fallback, the collector address
// as /32. The written argument was that "proxies querying this fleet are in this network
// or routed to it" — and it was that "or routed" that caused the defect, because a ROUTED
// proxy has an address OUTSIDE the plan subnet. The bound therefore excluded the only
// proxy meant to query the device.
//
// OBSERVED on the reference fleet's CRS326-24G-2S+, July 29 2026: conformant community,
// active agent, `addresses=10.99.0.0/16` — and silent walk. The device log gave the answer:
// the proxy reaches it from 10.42.0.67. An agent that rejects a source GOES QUIET, it does
// not protest, and silence does not distinguish "no agent" from "wrong source". The
// neighboring CSS326 responded, SwOS having no source-bound field: two platforms on the
// same chassis, only one reachable, which is the worst signal to interpret.
//
// The two sources therefore answer two different questions, and both are needed:
//
//  1. THE SITE MANAGEMENT SUBNET — a STABLE declaration by the operator. It survives proxy
//     redeployment, collector change, and address failover. It comes FIRST because a /32
//     alone would break polling SILENTLY on the next redeployment: new machine, new address,
//     agent rejects;
//  2. THE ADDRESS BY WHICH THE COLLECTOR REACHES THIS DEVICE, as /32 — the same question
//     asked for trap destination ([tunnel.AskSourceAddr]), for the same reason: only the
//     proxy kernel has the routing table that knows how to answer. It covers TODAY's proxy,
//     routed or not.
//
// THE /32 IS OMITTED WHEN THE SUBNET ALREADY CONTAINS IT, and that is not elegance:
// `addresses` is compared as a CHAIN to decide whether something must be written
// (`snmpConformant`). Adding a redundant /32 would rewrite the community across an already
// conformant fleet, on a platform where each write is a chance to err — and would do so on
// EVERY pass, the order of the two values needing to stay stable.
//
// # What remains unbounded
//
// EMPTY when neither is available, and that is deliberate: the contract says empty means
// "the caller said nothing", especially not "all". A platform that cannot push without a
// bound will then refuse by naming the missing gesture.
//
// The /32 requires a DECLARED collector: under coverage-based collection (`devices.proxy_id`
// empty), the device is queried by ALL proxies that cover its site — there is not one
// address to bound, there are several, and picking one at random would close the agent to
// the others. That is why the collector is now chosen AT ADOPTION (cf.
// [collectorForCandidate]): without it, this bound cannot be correct.
func (s *Server) snmpSourcesFor(ctx context.Context, d model.Device) []string {
	var out []string
	var plan, planErr = s.Store.GetMgmtPlan(ctx, d.SiteID)
	if planErr == nil && plan.Configured && plan.SubnetV4 != "" {
		out = append(out, plan.SubnetV4)
	}
	if s.Tunnels == nil || d.ProxyID == "" {
		return out
	}
	var reply, askErr = s.Tunnels.Ask(ctx, d.ProxyID, tunnel.AskRequest{
		Ask: tunnel.AskSourceAddr, IP: d.MgmtIP,
	})
	if askErr != nil || reply.SourceAddr == "" {
		return out
	}
	// A single host, expressed in the returned address family: /32 in v4, /128 in v6.
	// Writing /32 on an IPv6 address would give a prefix that does not encompass the
	// proxy address, therefore a silent agent for everyone.
	var host = reply.SourceAddr + "/32"
	if strings.Contains(reply.SourceAddr, ":") {
		host = reply.SourceAddr + "/128"
	}
	if coveredByAny(out, reply.SourceAddr) {
		return out
	}
	return append(out, host)
}

// coveredByAny reports whether one of the retained bounds already contains this address.
//
// An UNREADABLE address, or an unreadable bound, returns false: we prefer adding one /32
// too many — the agent then accepts an already-accepted source, which costs nothing — over
// omitting one and silencing the device for its collector. The direction of doubt is not
// symmetric here.
func coveredByAny(prefixes []string, addr string) bool {
	var ip, ipErr = netip.ParseAddr(strings.TrimSpace(addr))
	if ipErr != nil {
		return false
	}
	var p string
	for _, p = range prefixes {
		var pfx, err = netip.ParsePrefix(strings.TrimSpace(p))
		if err != nil {
			continue
		}
		if pfx.Contains(ip) {
			return true
		}
	}
	return false
}

// applySNMPToDevice pushes SNMP configuration. Returns the result and transcript so the
// caller decides what to do with them.
//
// EVERYTHING GOES THROUGH THE CONNECTOR DESIGNATED BY DISCOVERY, and it is this path that
// the founding batch bug took: adopting a MikroTik switch sent this push over INSTANT ON
// protocol toward it — `https://10.99.0.54/`, connection refused since that device does
// not listen on 443, `snmp.apply_failed` at severity 3. Nothing was written because TCP
// failed; the gesture itself had still started.
func (s *Server) applySNMPToDevice(ctx context.Context,
	d model.Device) (driver.SNMPResult, []driver.Step, error) {
	var out driver.SNMPResult
	if d.MgmtIP == "" {
		return out, nil, errNoMgmtIP
	}
	var want, ok = s.snmpIdentityToPose(ctx, d)
	if !ok {
		return out, nil, errNothingToPose
	}
	var conn, adm, _, err = s.adminFor(ctx, d)
	if err != nil {
		return out, transcriptOf(adm), err
	}
	// THE SNMP AGENT IS AN OPTIONAL CAPABILITY, and the type assertion is what allows
	// saying so instead of discovering it. It does NOT replace `Capabilities.SNMPVersions`:
	// the capability decides whether to push (`snmpprov.For` produced no supervised plan
	// without a declared version, and `snmpIdentityToPose` already returned false in that
	// case), the interface executes. Both must agree, and this refusal catches when they do
	// not — a connector that would announce versions without knowing how to bring supervision
	// up.
	var cfg, canConfigure = adm.(driver.SNMPConfigurator)
	if !canConfigure {
		return out, adm.Transcript(), noSNMPAgent{err: driver.NotSupported(
			conn.Info().Name, "push an SNMP identity on this device",
			"this connector cannot configure an SNMP agent: this platform's supervision "+
				"is not brought up by meshcraft. This is not a device fault, and no gesture "+
				"on it will change that")}
	}
	out, err = cfg.ApplySNMP(ctx, want)
	return out, adm.Transcript(), err
}

type apiError string

func (e apiError) Error() string { return string(e) }

// errNothingToPose: nothing to push is not a failure. The device cannot be supervised, or
// its identity is not generated yet — two cases the plan already explains.
const errNothingToPose = apiError("no SNMP identity to push on this device")

// noSNMPAgent wraps refusal from a platform that has NO pushable SNMP agent at all.
//
// A type, not a mere message, for a precise reason: this case is handled like
// `errNothingToPose` at provisioning — SILENTLY, it is an expected state for an entire
// platform — whereas a refusal returned BY `ApplySNMP` must surface in the log. Yet both
// can attach to [driver.ErrNotImplemented]: the RouterOS connector refuses SNMPv3 via
// [driver.NotSupported], and that refusal is a fact the operator must read (the device
// will not be supervised). Confusing them would silence one — and that is the one that
// matters.
//
// Same pattern as `trapAskError` (trapdest.go), applied to the same need: distinguish
// failures nothing else separates.
type noSNMPAgent struct{ err error }

func (e noSNMPAgent) Error() string { return e.err.Error() }
func (e noSNMPAgent) Unwrap() error { return e.err }

// provisionSNMP is the PROVISIONING gesture: generate identity if missing, push it, then
// open the RETURN path.
//
// Silent when all goes well, loud when it fails. An error never bubbles to the caller: an
// adopted device whose supervision could not be pushed remains adopted, and refusing
// adoption for that would be disproportionate. The log says so, and the push retries.
//
// # Why trap destination is here, and in this order
//
// Same reasoning as at the top of this file, applied to the second sense: adopting a
// device means making it administrable AND observable, and a trap channel left to manual
// gesture would guarantee a half-reactive fleet — each omission invisible, since a device
// that emits nothing is still supervised by polling, just thirty seconds later.
//
// Order is not negotiable: the connector REFUSES to declare a recipient on a device whose
// SNMP is off, and ApplySNMP turns it on. After identity failure we therefore attempt
// nothing — that would be a second failure whose cause is the first. And after
// `errNothingToPose` neither: without a pushable SNMP identity, there is no community to
// write in the destination, and it is the polling one that must go there (cf. trapdest.go,
// constraint 1).
func (s *Server) provisionSNMP(ctx context.Context, d model.Device) {
	s.ensureSNMPIdentity(ctx, d)

	var res, steps, err = s.applySNMPToDevice(ctx, d)
	if err == errNothingToPose {
		return
	}
	// A PLATFORM WITHOUT A PUSHABLE AGENT STAYS QUIET HERE, exactly like `errNothingToPose`.
	// This is not a provisioning failure: it is an expected state for an ENTIRE product line,
	// and it holds for every device on that line, at every adoption and every address change.
	// Logging it would drown the fleet log in a fact that does not change — same reasoning as
	// `snmpWatchesFor`, which excludes these devices from polling without emitting anything.
	//
	// The information is not lost, which makes silence acceptable: the device page "re-push
	// SNMP" button responds 422 with the full sentence (cf. handleApplyDeviceSNMP). It is
	// where you look for it.
	var noAgent noSNMPAgent
	if errors.As(err, &noAgent) {
		return
	}
	if err != nil {
		s.emitDeviceBG(d, "snmp.apply_failed", model.SevWarning,
			"SNMP configuration not pushed on \""+d.Name+"\": "+err.Error()+
				" — supervision will not start until it is pushed.")
		s.recordPipeline(ctx, pipelineForSNMP(d, steps, err, snmpSummary(res)), "")
		return
	}
	s.emitDeviceBG(d, "snmp.applied", model.SevInfo,
		"SNMP configuration pushed on \""+d.Name+"\": "+snmpSummary(res))
	s.recordPipeline(ctx, pipelineForSNMP(d, steps, nil, snmpSummary(res)), "")

	s.provisionTrapDest(ctx, d)
}

func snmpSummary(res driver.SNMPResult) string {
	var out = "SNMP "
	if res.Enabled {
		out += "enabled"
	} else {
		out += "already active"
	}
	if res.CommunitySet {
		out += ", read community set"
	}
	if res.UserSet {
		out += ", v3 user set"
	}
	if !res.CommunitySet && !res.UserSet {
		out += ", identity already conformant"
	}
	return out
}

// handleApplyDeviceSNMP re-pushes configuration on demand. Useful after a failure, or after
// rotating the supervision secret.
//
// TWO REFUSALS AS 422, NO LONGER AS `200 ok:false`, an assumed HTTP contract change aligned
// with handleApplyDeviceTrapDest:
//
//   - `errNothingToPose`: the device cannot be supervised, or its identity is not generated yet;
//   - `noSNMPAgent`: the connector cannot configure an agent at all.
//
// Neither is a write failure, and burying them in the same envelope as write failures forced
// the UI to READ TEXT to know whether to offer a button. 422 is the form the rest of the
// product uses for "your request is well formed but there is nothing to do, and here is why"
// (cf. ask_handlers.go).
func (s *Server) handleApplyDeviceSNMP(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var res, steps, err = s.applySNMPToDevice(r.Context(), d)
	var noAgent noSNMPAgent
	if err == errNothingToPose || errors.As(err, &noAgent) {
		respondErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	var out = map[string]any{"steps": steps, "result": res}
	if err != nil {
		out["ok"], out["error"] = false, err.Error()
		respond(w, http.StatusOK, out)
		return
	}
	out["ok"], out["summary"] = true, snmpSummary(res)
	s.emitDeviceEvent(r, d, "snmp.applied", model.SevNotice,
		"SNMP configuration pushed on \""+d.Name+"\": "+snmpSummary(res))
	respond(w, http.StatusOK, out)
}

// pipelineForSNMP makes the push inspectable afterward, like other operations: the only way
// to understand a failure without being at the screen when it happens.
func pipelineForSNMP(d model.Device, steps []driver.Step, failure error,
	summary string) store.Pipeline {
	var p = store.Pipeline{
		SiteID: d.SiteID, Kind: "monitoring", DeviceID: &d.ID,
		Steps: stepsFrom(steps),
	}
	if failure != nil {
		p.State = store.PipelineFailed
		p.Label = "SNMP configuration of \"" + d.Name + "\""
		p.BlockedReason = failure.Error()
		return p
	}
	p.State = store.PipelineDone
	p.Label = "SNMP pushed on \"" + d.Name + "\" — " + summary
	return p
}

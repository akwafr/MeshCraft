package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
	"meshcraft/internal/snmpprov"
	"meshcraft/internal/tunnel"
)

// SNMP diagnostic walk: POST /api/devices/{id}/snmp-walk.
//
// This is the CONTROLLER half of a one-off question sent to a proxy
// (tunnel.OpAsk). It exists because SNMP is UDP and the tunnel relays only
// TCP: there is nothing to dial, only the proxy can query the device, and
// without this route one would have to GUESS vendor MIB OIDs the device
// already knows how to serve.
//
// READ-ONLY, and not just by convention: the contract carries no write
// question and never will (cf. AskSNMPWalk in internal/tunnel/control.go).
// Configuration goes through connectors, which have their own guardrails —
// plan, dry-run, verification readback.
//
// The precedent it mirrors is POST /api/devices/{id}/web-probe: same role
// (tuning a connector on a real device), same privilege level (site_admin on
// the site PLUS global-admin), same lack of a dedicated screen. Exposing it
// lower would be an escalation: the primitive emits traffic from inside a
// client's network.
//
// What this route does NOT expose, and why: AskSourceAddr. The Hub method
// can ask it, but « which address does this proxy use to reach this device? »
// is not diagnostic information for an operator — it is internal data the
// product will use to derive a trap destination.
//
// Three refusals BEFORE any question, each with its reason:
//
//  1. the OID is validated here (normalizeOID) — a free-form field bound for
//     a production device deserves validation, global-admin or not;
//  2. the proxy is CHOSEN here (chooseAskProxy), and « nobody can query it »
//     is an answer, not silence;
//  3. the community comes from the VAULT (askCommunityFor), never from the
//     request.

// --- OID validation ---

const (
	// maxOIDArcs: 128 sub-identifiers, the RFC 3416 bound for an ObjectName.
	// Beyond that it is no longer an OID: it is an input field that overflowed,
	// and refusing it here avoids having the proxy build a packet no agent will
	// accept.
	maxOIDArcs = 128
	// maxOIDArc: a sub-identifier fits in unsigned 32 bits (RFC 2578 §7.1.3).
	// This bound catches an interface index pasted by mistake at the end of an OID.
	maxOIDArc = "4294967295"
)

// normalizeOID accepts a plain OID and returns its canonical form, or says
// why it is not one.
//
// Pure, and that is the rule for the batch: this decision must be exercised
// without a database, proxy, or device (ask_handlers_test.go).
//
// NUMERIC only. Accepting « ifDescr » or « 1.3.6.1.2.1.2.2.1.* » would require
// embedding a MIB compiler in the controller; refusing them WITH a reason is
// better than forwarding them so the proxy answers « invalid OID » from the
// other end of the WAN. A leading dot is tolerated because most vendor docs
// write OIDs that way, and it is stripped: what goes on the wire must have
// a single form.
func normalizeOID(raw string) (string, error) {
	var oid = strings.TrimSpace(raw)
	if oid == "" {
		return "", errors.New("oid required: the root to walk, in NUMERIC form — " +
			"« 1.3.6.1.2.1.2.2 » for ifTable, for example")
	}
	oid = strings.TrimPrefix(oid, ".")
	var arcs = strings.Split(oid, ".")
	if len(arcs) < 2 {
		// An OID has at least two arcs: that is true of its ASN.1 encoding, and a
		// lone « 1 » would ask a switch to dump its entire MIB.
		return "", fmt.Errorf("oid %q: at least two arcs expected "+
			"(« 1.3.6.1… »); a single-arc OID does not exist", raw)
	}
	if len(arcs) > maxOIDArcs {
		return "", fmt.Errorf("oid %q: %d arcs, beyond the %d an SNMP "+
			"ObjectName may carry", raw, len(arcs), maxOIDArcs)
	}
	var i int
	for i = range arcs {
		var arc = arcs[i]
		if arc == "" {
			return "", fmt.Errorf("oid %q: empty arc (two dots in a row, or a "+
				"trailing dot)", raw)
		}
		if len(arc) > 1 && arc[0] == '0' {
			// « 1.3.06 » is not « 1.3.6 » for everyone: refuse rather than choose
			// on the author's behalf.
			return "", fmt.Errorf("oid %q: arc « %s » starts with a zero, "+
				"write it without", raw, arc)
		}
		var j int
		for j = range arc {
			if arc[j] < '0' || arc[j] > '9' {
				return "", fmt.Errorf("oid %q: « %s » is not numeric. This path "+
					"interprets no MIB name and no wildcard: give the OID in digits, "+
					"as vendor documentation does", raw, arc)
			}
		}
		var _, convErr = strconv.ParseUint(arc, 10, 32)
		if convErr != nil {
			return "", fmt.Errorf("oid %q: arc « %s » exceeds %s, the maximum "+
				"SNMP sub-identifier", raw, arc, maxOIDArc)
		}
	}
	// The first arc of an OID is 0 (ccitt), 1 (iso), or 2 (joint-iso-ccitt).
	// Everything from a real MIB starts with 1; checking that catches IP addresses
	// pasted into the OID field, the most likely typo on this screen.
	if arcs[0] != "0" && arcs[0] != "1" && arcs[0] != "2" {
		return "", fmt.Errorf("oid %q: the first arc must be 0, 1, or 2 — device "+
			"OIDs all start with 1 (« 1.3.6.1… »). Did you enter an IP address in "+
			"the OID field?", raw)
	}
	return oid, nil
}

// --- Which proxy is queried ---

// askProxyVerdict: who may query this device?
type askProxyVerdict int

const (
	// askProxyAttached: the device has a declared COLLECTOR that covers its site.
	// It alone is used.
	askProxyAttached askProxyVerdict = iota
	// askProxyCovering: no attachment — coverage regime. Any covering proxy will
	// do; we pick one.
	askProxyCovering
	// askProxyNoCoverage: no proxy covers the site. Nobody collects this device,
	// so nobody can query it.
	askProxyNoCoverage
	// askProxyNotCovering: a collector is declared but no longer covers the site.
	// This is the « collected by nobody » case the device page already signals
	// (cf. devices_handlers.go, § Attachment).
	askProxyNotCovering
)

// chooseAskProxy picks the proxy to ask.
//
// Pure — like judgeCollector, its little sibling, and it reuses exactly the
// notion of coverage: `covering` is the output of Store.ProxiesCoveringSite,
// i.e. the SAME predicate that decides who collects (`p.global or exists(proxy_sites…)`).
// Nothing is reinvented here, and that is deliberate: a walk from a proxy that
// does not cover the site would bypass the confidentiality boundary of migration 0022.
//
// Rule in one sentence: an attached device is queried by ITS collector; an
// unattached device by a proxy that covers it.
//
// No substitution for an attached device, and that is the point worth stating:
// when its collector is no longer covering, we REFUSE instead of picking
// another proxy. A walk answered from another point in the network is an answer
// to a different question — and above all it would hide the real problem
// (the device is collected by nobody) behind a diagnosis that works.
//
// `connected` breaks ties among covering proxies when there are several:
// querying a proxy without a tunnel would yield « no tunnel established » when
// another would have answered. Nil = unknown, take the first — ProxiesCoveringSite
// order (instance proxies first) is already stable, so the choice is reproducible.
func chooseAskProxy(covering []model.Proxy, attached string,
	connected func(string) bool) (askProxyVerdict, model.Proxy) {
	var p model.Proxy
	if attached != "" {
		for _, p = range covering {
			if p.ID == attached {
				return askProxyAttached, p
			}
		}
		return askProxyNotCovering, model.Proxy{}
	}
	if len(covering) == 0 {
		return askProxyNoCoverage, model.Proxy{}
	}
	if connected != nil {
		for _, p = range covering {
			if connected(p.ID) {
				return askProxyCovering, p
			}
		}
	}
	// No established tunnel among covering proxies: still return a proxy so the
	// failure names the ONE that should have been up rather than a generic « no
	// proxy » that sends people looking for missing coverage.
	return askProxyCovering, covering[0]
}

// noAskProxyMessage says what is missing and the gesture that fixes it.
//
// The two refusals look alike and are not repaired the same way — widen a
// proxy's coverage, or detach the device — hence two sentences rather than a
// generic « no proxy available » that would fit both without helping either.
func noAskProxyMessage(verdict askProxyVerdict, deviceName, collectorName string) string {
	if verdict == askProxyNotCovering {
		var who = "its declared collector"
		if collectorName != "" {
			who = "its declared collector (« " + collectorName + " »)"
		}
		return "Question impossible: " + who + " does not cover the site of « " +
			deviceName + " ». This device is therefore collected by nobody — neither " +
			"polled in SNMP nor a trap recipient — and meshcraft will not ask from " +
			"another proxy: the answer would come from another point in the network " +
			"and hide the problem. Add this site to the proxy's coverage, or detach " +
			"the device to return it to coverage-by-site regime."
	}
	return "Question impossible: no proxy covers the site of « " + deviceName +
		" ». SNMP is UDP; the tunnel relays only TCP: only a proxy deployed in " +
		"this network can query this device. Deploy one, or extend an existing " +
		"proxy's coverage to this site."
}

// --- SNMP community from the vault ---

// askSNMPVerdict: is this device queryable through THIS path?
type askSNMPVerdict int

const (
	// askSNMPReady: v2c plan, credential present in the vault.
	askSNMPReady askSNMPVerdict = iota
	// askSNMPUnsupervised: no usable SNMP plan — unknown connector, or connector
	// declaring no version.
	askSNMPUnsupervised
	// askSNMPv3Only: the device is supervised in v3. The walk is v2c: there is
	// no community to present.
	askSNMPv3Only
	// askSNMPNoCredential: v2c plan, but nothing in the vault for this site.
	askSNMPNoCredential
)

// judgeAskSNMP decides whether a device is queryable in v2c, and says why not.
//
// Pure, and that is what matters: the package has no database in tests, and
// this is where the refusal an operator reads is taken. It reuses exactly the
// conditions of snmpWatchesFor (watch_handlers.go) — v2c plan, credential
// present — because a manually queryable device and a continuously collected
// device must be the same set. Two diverging rules would give a diagnosis that
// works on a device that reports nothing.
//
// The difference is SILENCE: collection drops without a word (three log lines
// per proxy connection, indefinitely, for a state that does not change), whereas
// here someone waits for an answer on screen. Every refusal therefore carries
// its reason.
func judgeAskSNMP(deviceName string, plan snmpprov.Plan,
	credentialID string) (askSNMPVerdict, string) {
	if !plan.Supervised() {
		var why = plan.Reason
		if why == "" {
			why = "no SNMP version is known for this device."
		}
		return askSNMPUnsupervised, "Walk impossible on « " + deviceName + " »: " +
			why + " A walk targets an SNMP agent; meshcraft does not know whether " +
			"this device has one."
	}
	if plan.Kind == snmpprov.KindV3 {
		// Refuse rather than try: presenting a community to an agent that has none
		// would produce a timeout — a diagnosis that accuses the device of being
		// mute when it is perfectly right to stay silent.
		return askSNMPv3Only, "Walk impossible on « " + deviceName + " »: this " +
			"device is supervised in SNMPv3, and this diagnostic path speaks only " +
			"v2c (like the proxy collector, cf. internal/snmppoll). There is no " +
			"community to present — querying anyway would only yield a timeout. The " +
			"v3 question will come with service-identity transport."
	}
	if plan.Kind != snmpprov.KindV2c {
		return askSNMPUnsupervised, "Walk impossible on « " + deviceName +
			" »: this device's SNMP plan designates no credential kind queryable " +
			"through this path (« " + plan.Kind + " »)."
	}
	if credentialID == "" {
		return askSNMPNoCredential, "Walk impossible on « " + deviceName + " »: " +
			"no SNMPv2c community exists in the vault for its site. The community " +
			"comes from the vault and never from the request — generate the device's " +
			"SNMP identity (snmp-apply) or enter the community in site credentials, " +
			"then ask again."
	}
	return askSNMPReady, ""
}

// askCommunityFor resolves a device's v2c community.
//
// Same pattern as snmpWatchesFor, identically: SNMP plan, vault credential ID,
// decryption. The secret is passed BY OPERATION to the question sent to the
// proxy (cf. AskRequest.Community) — the proxy receives neither the vault nor
// a fleet credential ID, only an address and a community, for the duration of
// one question.
//
// Three returns because three kinds: the community, the readable REFUSAL (a
// normal operations situation, returned as 422 with its reason), and the
// technical error (returned via `fail`, the package's only translation table).
func (s *Server) askCommunityFor(ctx context.Context, d model.Device) (string, string, error) {
	if s.Secrets == nil {
		return "", "Walk impossible: this instance's vault is unavailable, " +
			"yet the SNMP community can come only from it.", nil
	}
	var st, err = s.snmpPlanFor(ctx, d)
	if err != nil {
		return "", "", err
	}
	var verdict, refusal = judgeAskSNMP(d.Name, st.Plan, st.CredentialID)
	if verdict != askSNMPReady {
		return "", refusal, nil
	}
	// Second phrase deliberately ignored: we reach here only for v2c, where the
	// community IS the secret and there is never a second one.
	var _, enc, _, revErr = s.Store.RevealSiteCredential(ctx, st.CredentialID)
	if revErr != nil {
		return "", "", revErr
	}
	var community string
	if community, err = s.Secrets.Open(enc); err != nil || community == "" {
		return "", "Walk impossible on « " + d.Name + " »: the site's SNMPv2c " +
			"community is in the vault but unreadable (encryption key changed?). " +
			"This is the same cause that deprives this device of SNMP collection.", nil
	}
	return community, "", nil
}

// --- Translating question failures ---

// askFailure translates a question failure into an HTTP response.
//
// Pure, and tested: four failures repaired by four different gestures must
// produce four different responses. Conflating them — as a single 502 « the
// question failed » would — sends the operator to check the device when they
// should restart a proxy, or the reverse.
//
// Sorting uses `tunnel` sentinels, not message text: string-based sorting does
// not survive the first rewording.
func askFailure(err error) (int, string) {
	var remote *tunnel.AskRemoteError
	switch {
	case errors.Is(err, tunnel.ErrNoTunnel):
		// 503: nothing is broken, the proxy is not there. Same message as
		// everywhere else in the product (cf. dialerFor) — an operator should not
		// learn two formulations of the same incident.
		return http.StatusServiceUnavailable, errNoTunnel.Error()
	case errors.As(err, &remote):
		// The proxy spoke: its diagnosis comes from where the attempt happened; it
		// beats anything we could write here.
		return http.StatusBadGateway, "The proxy could not respond: " +
			firstLine(remote.Detail)
	case errors.Is(err, tunnel.ErrAskTimeout):
		// 504 not 502: nobody said no, the deadline simply passed.
		return http.StatusGatewayTimeout, firstLine(err.Error()) +
			". The device may not respond on 161/udp, or its community may not " +
			"match the vault — an agent that rejects a community stays silent, " +
			"it does not protest."
	case errors.Is(err, tunnel.ErrAskClosed):
		return http.StatusBadGateway, firstLine(err.Error()) +
			": this proxy probably does not yet answer one-off questions. " +
			"Upgrade the proxy version."
	case errors.Is(err, tunnel.ErrAskUnreadable):
		return http.StatusBadGateway, firstLine(err.Error()) +
			" — protocol fault between controller and this proxy, not a problem " +
			"with the queried device."
	}
	return http.StatusBadGateway, "Question to proxy failed: " + firstLine(err.Error())
}

// --- Handler ---

func (s *Server) handleDeviceSNMPWalk(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	// Same reserve as the web probe: the primitive emits traffic from inside a
	// client's network, to an address chosen in the request.
	if !auth.UserFrom(r.Context()).IsGlobalAdmin {
		respondErr(w, http.StatusForbidden,
			"SNMP walk reserved for global administration")
		return
	}
	var in struct {
		OID string `json:"oid"`
		// MaxRows: a WISH, clamped to the contract's hard ceiling.
		MaxRows int `json:"max_rows"`
		Port    int `json:"port"`
		// TimeoutMS: desired wait, bounded by tunnel.MaxAskWait.
		TimeoutMS int `json:"timeout_ms"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}
	var oid, oidErr = normalizeOID(in.OID)
	if oidErr != nil {
		respondErr(w, http.StatusBadRequest, oidErr.Error())
		return
	}
	if in.Port < 0 || in.Port > 65535 {
		respondErr(w, http.StatusBadRequest, "port out of 0-65535 range (0 = 161, the SNMP port)")
		return
	}
	if d.MgmtIP == "" {
		respondErr(w, http.StatusUnprocessableEntity, "« "+d.Name+" » has no "+
			"management address: there is nothing to query.")
		return
	}

	// The proxy is chosen HERE, controller-side. The proxy itself receives only
	// an address: it must not know fleet identifiers (cf. AskRequest.IP).
	var covering, err = s.Store.ProxiesCoveringSite(r.Context(), d.SiteID)
	if err != nil {
		fail(w, err)
		return
	}
	var connected func(string) bool
	if s.Tunnels != nil {
		connected = s.Tunnels.Connected
	}
	var verdict, target = chooseAskProxy(covering, d.ProxyID, connected)
	if verdict == askProxyNoCoverage || verdict == askProxyNotCovering {
		respondErr(w, http.StatusUnprocessableEntity,
			noAskProxyMessage(verdict, d.Name, d.ProxyName))
		return
	}

	var community, refusal, credErr = s.askCommunityFor(r.Context(), d)
	if credErr != nil {
		fail(w, credErr)
		return
	}
	if refusal != "" {
		respondErr(w, http.StatusUnprocessableEntity, refusal)
		return
	}

	// The ceiling is applied here IN ADDITION to the Hub, so the caller gets the
	// bound actually used: a 500-line response to a 50,000-row request would read
	// as a complete table.
	var rows = tunnel.ClampAskRows(in.MaxRows)
	// No context shorter than the request's: the Hub bounds the wait
	// (tunnel.ClampAskWait), and a second budget here would be two places to read
	// the same rule.
	var reply, askErr = s.Tunnels.Ask(r.Context(), target.ID, tunnel.AskRequest{
		Ask: tunnel.AskSNMPWalk, IP: d.MgmtIP, Community: community,
		Port: in.Port, OID: oid, MaxRows: rows, TimeoutMS: in.TimeoutMS,
	})
	if askErr != nil {
		var code, msg = askFailure(askErr)
		respondErr(w, code, msg)
		return
	}
	// Fleet journal, like reading a device page: this is a manual query of a
	// production device, and there must be a trace of who asked. SevInfo — nothing
	// changed on the device.
	s.emitDeviceEvent(r, d, "device.snmp_walk", model.SevInfo,
		"SNMP walk of "+oid+" on « "+d.Name+" » via proxy « "+target.Name+
			" »: "+itoa(len(reply.Rows))+" values")
	respond(w, http.StatusOK, map[string]any{
		"oid": oid, "ip": d.MgmtIP,
		"proxy_id": target.ID, "proxy_name": target.Name,
		// The bound RETAINED, not the one requested: that is what makes `truncated`
		// interpretable.
		"max_rows": rows,
		"count":    len(reply.Rows),
		// Rows may be nil when the device serves nothing under this OID: an empty
		// list reads better than `null` in a tuning tool.
		"rows":      rowsOrEmpty(reply.Rows),
		"truncated": reply.Truncated,
	})
}

func rowsOrEmpty(rows []tunnel.AskRow) []tunnel.AskRow {
	if rows == nil {
		return []tunnel.AskRow{}
	}
	return rows
}

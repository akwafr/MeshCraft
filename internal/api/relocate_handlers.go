package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/discovery"
	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/relocate"
)

// Find a device that changed address
// (cf. docs/01-Architecture/Management addressing section 5 and internal/relocate).
//
// Two parallel paths: scanning the target subnet, and the address the operator
// enters if they already know it. The first to identify the correct SERIAL
// NUMBER wins, and the management address is updated.

type relocateInput struct {
	// Prefix: where to search. By default, the management plan subnet for the
	// site — that is where the switchover just sent the device.
	Prefix string `json:"prefix"`
	// ProxyID: through which proxy to search. By default, a proxy that reaches this network.
	ProxyID string `json:"proxy_id"`
}

// serialOf builds the function that, for an address, says which device responds.
// It goes through the connector: that is what knows how to read an identity.
//
// # THE MOST SENSITIVE POINT IN THE BATCH: this scan sees UNKNOWN machines
//
// Relocation hits ALL live addresses in a prefix — a /24, and nothing stops a /16.
// We do not know what they are: a workstation, a NAS, another client's device on a
// poorly segmented network, a honeypot.
//
// Yet the three platforms do not identify at the same cost:
//
//   - Instant On: NO credential. Its login page carries `sysDescr` and the serial
//     number in hidden fields. That is what made relocation possible without knowing
//     anything about the device;
//   - SwOS: the MODEL without a credential (its Digest challenge realm carries it),
//     but the SERIAL requires logging in — and the serial is the only field that counts;
//   - RouterOS: NOTHING without a credential. Its REST API returns 401 on all resources.
//
// Making MikroTik relocation work would therefore mean presenting an administration
// secret to every live address in the prefix. UNACCEPTABLE: a secret exposed to unknown
// machines is a secret to treat as burned, and there is no "but we only showed it once"
// that holds.
//
// # THE RULE, MORE RESTRICTIVE THAN "TRY EVERYWHERE"
//
//  1. identification WITHOUT a credential first, always. If it returns a serial, done —
//     that is the Instant On path, and it presents nothing;
//  2. otherwise, we only present a credential TO ADDRESSES WHOSE ANONYMOUS FINGERPRINT
//     ALREADY DESIGNATES THIS CONNECTOR. That fingerprint exists and costs nothing: the
//     web page TITLE is served without authentication on both MikroTik platforms, and
//     that is what `discovery.Prober` reads to rank a candidate.
//     An address not recognized as the sought platform never sees any credential;
//  3. and only then ONE candidate, the ACTIVE generation of the sought device's
//     credential. Never the ordered list — it would include retiring generations —,
//     never the factory account with an empty password, which would tell a stranger
//     which account meshcraft is trying.
//
// If that single candidate could not be built, relocation for this platform is
// UNAVAILABLE and says so (cf. `credRefusal`): it does not fail silently across
// hundreds of addresses.
func (s *Server) serialOf(driverName string, dialer discovery.Dialer,
	cand *driver.Candidate, credRefusal string) relocate.Probe {
	return func(ctx context.Context, ip string) (string, error) {
		// LIVENESS TEST FIRST, and short. Querying the web UI of a dead address costs
		// two timeouts (~30 s): on a /16, the search would take hours. A 700 ms dial
		// eliminates 99.9% of addresses at once.
		// Measured: 1 address / 5 s before, ~200 / s after.
		if !aliveFast(ctx, ip, dialer) {
			return "", nil
		}
		var conn, connErr = driver.ConnectorFor(driverName)
		if connErr != nil {
			return probeSerialAnon(ctx, ip, dialer)
		}
		var ident, canIdentify = conn.(driver.Identifier)
		if !canIdentify {
			// The connector can write but not say WHO responds at an address. The generic
			// probe remains the best fallback: it only returns a serial number if the device
			// publishes it without authentication.
			return probeSerialAnon(ctx, ip, dialer)
		}
		var tgt = driver.Target{Host: ip, Dialer: dialer}

		// STEP 1 — without any credential. The connector that does not need one responds
		// here, and no secret has circulated.
		var id, err = ident.Identify(ctx, tgt, nil)
		if id.Serial != "" {
			return id.Serial, nil
		}
		// STEP 2 — the ANONYMOUS fingerprint must designate this platform, otherwise we
		// stop there. An address we know nothing about is not a failure: it is an address
		// that is not the one we seek, and the scan continues.
		if !anonLooksLike(ctx, ip, dialer, driverName) {
			return "", nil
		}
		if cand == nil {
			// Unavailable, and we must SAY SO: this is the only path by which an operator
			// learns why their MikroTik was not found. The message surfaces on manual
			// address submission (502), where someone reads it.
			return "", errors.New(credRefusal)
		}
		// STEP 3 — ONE candidate, and only one.
		id, err = ident.Identify(ctx, tgt, []driver.Candidate{*cand})
		if err != nil && id.Serial == "" {
			return "", err
		}
		return id.Serial, nil
	}
}

// probeSerialAnon: the generic probe, WITHOUT credential.
//
// Fallback when the connector cannot identify — or when there is none. It only returns
// a serial number if the device publishes it on its own (SSH banner, title, command
// output), which is rare, but that rarity costs no secret.
func probeSerialAnon(ctx context.Context, ip string, dialer discovery.Dialer) (string, error) {
	var prober = &discovery.Prober{Dialer: dialer}
	var res = prober.Probe(ctx, ip, nil)
	if !res.Reachable {
		return "", nil
	}
	return res.Serial, nil
}

// anonLooksLike: does the ANONYMOUS fingerprint of this address designate this connector?
//
// THE GUARDRAIL THAT DECIDES WHETHER A SECRET MAY BE PRESENTED HERE, and it only uses
// what an anonymous caller gets: open ports, SSH banner, header and TITLE of the web
// page. On both MikroTik platforms the title is served without authentication — observed
// on hardware, and that is what discovery already uses to rank a candidate in its line.
//
// The predicate is deliberately connector name EQUALITY, not resemblance: we seek a
// specific device whose connector we know, and "this might be a MikroTik" does not
// authorize showing a password.
func anonLooksLike(ctx context.Context, ip string, dialer discovery.Dialer,
	driverName string) bool {
	var prober = &discovery.Prober{Dialer: dialer}
	var res = prober.Probe(ctx, ip, nil)
	return res.Reachable && res.Driver == driverName
}

// relocateCandidate builds the ONLY credential we will present during a relocation scan,
// or explains why there is none.
//
// ONE ONLY, AND THE ACTIVE GENERATION ONLY. `candidatesFor` also returns the ORDERED list
// — observed generation, retiring generations, factory account — and it is deliberately
// DISCARDED here: that list is meant to be presented to a device we know is the right one,
// not machines we are discovering. Showing it to a stranger would expose the fleet's
// secret history and the account meshcraft tries, to find a single switch.
//
// The second return is a readable REFUSAL, not a boolean: "no active generation" and
// "no credential applies to this device" are fixed by different actions, and the operator
// who enters their MikroTik's address must read which case applies.
func (s *Server) relocateCandidate(ctx context.Context,
	d model.Device) (*driver.Candidate, string) {
	var indispo = "Relocation unavailable on this platform: its connector " +
		"can only establish the serial number WITH a credential, and meshcraft has " +
		"none usable for this device. "
	var conn, connErr = s.connectorFor(ctx, d)
	if connErr != nil {
		return nil, indispo + connErr.Error()
	}
	var groupName = s.groupNameOfCtx(ctx, d)
	var cred, _, credErr = s.credentialForDevice(ctx, d, groupName)
	if credErr != nil {
		return nil, indispo + credErr.Error() +
			" — declare an administration credential for its site, then retry."
	}
	// The deployable name, not the desired name: on a platform without a named account,
	// presenting `mcadmin` would fail authentication on a name that does not exist, and
	// the failure would read "password refused".
	var wanted = conn.Capabilities(d.Model).Admin.EffectiveUsername(cred.Username)
	var _, target, gen, _, buildErr = s.candidatesFor(ctx, d, wanted, groupName, "")
	if buildErr != nil {
		return nil, indispo + buildErr.Error()
	}
	if target == "" {
		return nil, indispo + "no active generation for its credential."
	}
	return &driver.Candidate{
		Username: wanted, Secret: target,
		Why: "active generation " + itoa(gen) + ", only candidate allowed during a " +
			"relocation scan",
	}, ""
}

// aliveFast: is there something at this address? An administration web or SSH port is
// enough to tell. We are not identifying here, only ruling out empty addresses.
func aliveFast(ctx context.Context, ip string, dialer discovery.Dialer) bool {
	var ports = []int{443, 80, 22}
	var found = make(chan bool, len(ports))
	var port int
	for _, port = range ports {
		go func(prt int) {
			var dctx, cancel = context.WithTimeout(ctx, 700*time.Millisecond)
			defer cancel()
			var addr = net.JoinHostPort(ip, strconv.Itoa(prt))
			var conn net.Conn
			var err error
			if dialer != nil {
				conn, err = dialer.DialContext(dctx, "tcp", addr)
			} else {
				var d = net.Dialer{Timeout: 700 * time.Millisecond}
				conn, err = d.DialContext(dctx, "tcp", addr)
			}
			if err == nil {
				_ = conn.Close()
				found <- true
				return
			}
			found <- false
		}(port)
	}
	var i int
	for i = 0; i < len(ports); i++ {
		if <-found {
			return true
		}
	}
	return false
}

// handleStartRelocate starts searching for a device.
func (s *Server) handleStartRelocate(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "operator")
	if !ok {
		return
	}
	var in relocateInput
	if !decodeOptional(w, r, &in) {
		return
	}

	// The sought identity comes from the original discovery candidate: that is where the
	// serial number was observed.
	var serial, driverName = s.deviceIdentity(r.Context(), d)
	if serial == "" {
		respondErr(w, http.StatusUnprocessableEntity,
			"this device has no known serial number: it cannot be recognized at another "+
				"address. Find it with a scan and re-adopt it, or enter its address directly "+
				"on its page.")
		return
	}

	var prefix = strings.TrimSpace(in.Prefix)
	if prefix == "" {
		var plan, planErr = s.Store.GetMgmtPlan(r.Context(), d.SiteID)
		if planErr != nil {
			fail(w, planErr)
			return
		}
		prefix = plan.SubnetV4
	}
	if prefix == "" {
		respondErr(w, http.StatusUnprocessableEntity,
			"no subnet to scan: fill in the site management plan or specify a range explicitly")
		return
	}
	var addrs, expandErr = discovery.ExpandPrefix(prefix)
	if expandErr != nil {
		respondErr(w, http.StatusUnprocessableEntity, expandErr.Error())
		return
	}

	var proxyID = in.ProxyID
	if proxyID == "" {
		proxyID = d.ProxyID
	}
	if proxyID == "" {
		// Same path apply/jump use: the proxy that discovered the device, not
		// Local by default — a site-only CSS326 is invisible to the controller host.
		proxyID = s.proxyOf(r.Context(), d)
	}
	var dialer, dialErr = s.dialerFor(proxyID)
	if dialErr != nil {
		respondErr(w, http.StatusServiceUnavailable, dialErr.Error())
		return
	}

	// The SINGLE candidate, built ONCE here and not per address: resolving it at the
	// bottom of the scan would hit the database for each of 254 (or 65,534) addresses,
	// and especially decide there which secret to present to a stranger.
	// The refusal travels with it: it is only spoken at an address whose anonymous
	// fingerprint clearly designates the sought platform, i.e. where it informs.
	var cand, credRefusal = s.relocateCandidate(r.Context(), d)

	var st = s.Relocate.Start(d.ID, serial, prefix, addrs,
		discovery.ConcurrencyFor(len(addrs)),
		s.serialOf(driverName, dialer, cand, credRefusal),
		s.onRelocated(d))
	s.emitSite(r, d.SiteID, "device.relocate_started", model.SevNotice,
		"Searching for \""+d.Name+"\" (serial "+serial+") in "+prefix, nil)
	respond(w, http.StatusAccepted, st)
}

// onRelocated updates the management address when the device is found. The request
// context is dead by then: we create a new one.
func (s *Server) onRelocated(d model.Device) relocate.OnFound {
	return func(deviceID, ip, foundBy string) {
		var ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var err = s.Store.SetDeviceMgmtIP(ctx, deviceID, ip)
		if err != nil {
			slog.Error("relocation: address not recorded",
				"err", err, "device", deviceID, "ip", ip)
			return
		}
		if s.Renumber != nil {
			s.Renumber.Forget(deviceID)
		}
		var fresh, getErr = s.Store.GetDevice(ctx, deviceID)
		if getErr == nil {
			s.pushWatchForDevice(fresh)
		}
		var by = "the scan"
		if foundBy == "operator" {
			by = "the operator"
		}
		slog.Info("device found", "device", d.Name, "ip", ip, "by", foundBy)
		var siteID = d.SiteID
		_ = s.Store.InsertEvent(ctx, model.Event{
			SiteID: &siteID, DeviceID: &deviceID, Type: "device.relocated",
			Severity: model.SevNotice,
			Message: "Device \"" + d.Name + "\" found at " + ip +
				" (found by " + by + ")",
			Actor: "meshcraft",
		})
	}
}

// kickRelocateAfterMgmtWrite starts a subnet search when meshcraft moved
// management addressing but does not know the new reachability IP.
//
// hintIPs, when known (prepare-phase address, last observed lease…), are also
// pushed onto the proxy watch list so ICMP can confirm them before/while the
// subnet scan runs — without promoting on ping alone.
//
// Used after DHCP apply/jump and after Instant On migrate when the post-commit
// lease may differ from the prepare-phase address. Static writes adopt the
// intent IP directly (cf. adoptWrittenMgmtIP) and do not need this.
//
// Best-effort: missing serial or subnet returns a note; it never fails the
// write that already succeeded on the device.
func (s *Server) kickRelocateAfterMgmtWrite(ctx context.Context, d model.Device,
	why string, hintIPs ...string) string {
	s.rememberRenumberCandidate(d, hintIPs...)
	var serial, driverName = s.deviceIdentity(ctx, d)
	if serial == "" {
		return "relocate manually: this device has no known serial number"
	}
	var plan, planErr = s.Store.GetMgmtPlan(ctx, d.SiteID)
	if planErr != nil || strings.TrimSpace(plan.SubnetV4) == "" {
		return "relocate manually: no management subnet to scan"
	}
	var prefix = strings.TrimSpace(plan.SubnetV4)
	var addrs, expandErr = discovery.ExpandPrefix(prefix)
	if expandErr != nil {
		return "relocate manually: " + expandErr.Error()
	}
	var proxyID = d.ProxyID
	if proxyID == "" {
		proxyID = s.proxyOf(ctx, d)
	}
	var dialer, dialErr = s.dialerFor(proxyID)
	if dialErr != nil {
		return "relocate manually: " + dialErr.Error()
	}
	var cand, credRefusal = s.relocateCandidate(ctx, d)
	_ = s.Relocate.Start(d.ID, serial, prefix, addrs,
		discovery.ConcurrencyFor(len(addrs)),
		s.serialOf(driverName, dialer, cand, credRefusal),
		s.onRelocated(d))
	var siteID = d.SiteID
	_ = s.Store.InsertEvent(ctx, model.Event{
		SiteID: &siteID, DeviceID: &d.ID, Type: "device.relocate_started",
		Severity: model.SevNotice,
		Message: "Searching for \"" + d.Name + "\" after " + why +
			" (serial " + serial + ") in " + prefix,
		Actor: "meshcraft",
	})
	return "searching for the device in " + prefix
}

// deviceIdentity recovers a device's serial number and connector by going back through
// the discovery candidate it came from.
func (s *Server) deviceIdentity(ctx context.Context, d model.Device) (serial, driverName string) {
	driverName = driver.DriverNameFromExtra(d.Extra)
	var discoveredID = driver.ExtraString(d.Extra, "discovered_id")
	if discoveredID == "" {
		return "", driverName
	}
	var cand, err = s.Store.GetDiscovered(ctx, discoveredID)
	if err != nil {
		return "", driverName
	}
	if driverName == "" {
		driverName = cand.Driver
	}
	return cand.Serial, driverName
}

func (s *Server) handleGetRelocate(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "viewer")
	if !ok {
		return
	}
	var st, found = s.Relocate.Get(d.ID)
	if !found {
		respondErr(w, http.StatusNotFound, "no search in progress")
		return
	}
	respond(w, http.StatusOK, st)
}

// handleSubmitRelocate: the operator knows the address. It is verified like those from
// the scan — entering the wrong address would point meshcraft at another device.
func (s *Server) handleSubmitRelocate(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "operator")
	if !ok {
		return
	}
	var in struct {
		IP string `json:"ip"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.IP = strings.TrimSpace(in.IP)
	if in.IP == "" {
		respondErr(w, http.StatusBadRequest, "ip required")
		return
	}
	var ctx, cancel = context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var st, err = s.Relocate.Submit(ctx, d.ID, in.IP)
	switch {
	case err == nil:
		respond(w, http.StatusOK, st)
	case err == relocate.ErrNotSearching:
		respondErr(w, http.StatusNotFound, err.Error())
	case err == relocate.ErrSerialMismatch:
		respondErr(w, http.StatusConflict,
			"a device responds at "+in.IP+" but it is not this one "+
				"(serial number mismatch): the address was not kept")
	default:
		respondErr(w, http.StatusBadGateway, firstLine(err.Error()))
	}
}

func (s *Server) handleCancelRelocate(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "operator")
	if !ok {
		return
	}
	s.Relocate.Cancel(d.ID)
	respond(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// deviceForWrite is defined in devices_handlers.go; this reminder avoids a future file
// move breaking this module's compile.
var _ = chi.URLParam

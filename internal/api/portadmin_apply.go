package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// The two destructive operator gestures: shut a port, and bounce a port's PoE
// so the thing on the other end reboots.
//
// # Neither of them is an intent, and neither is reconciled
//
// internal/driver/portadmin.go states the rule and this file only carries it up
// to HTTP: ApplyPorts sets a CONFIGURATION that drift compares, these set
// nothing. A shut port stored as an intent would be re-shut on every pass, and
// a power cycle has no steady state at all — it is over by the time the panel
// repaints. So nothing here lands in `devices.ports`, nothing here is diffed,
// and there is NO automatic sibling of these two handlers. They are reachable
// only from an explicit click, and the apply pipeline must never grow a call to
// them.
//
// # Why they are two endpoints and not one button with a mode
//
// Because they do opposite things and get confused for each other. A link
// bounce drops ethernet: the far-end device keeps running, its network goes
// away and comes back. A power cycle cuts the volts: the far-end device
// REBOOTS. One button for both would eventually reboot a building's worth of
// access points to fix a link light, which is exactly the trade the driver
// contract refuses to let a caller make by accident.
//
// # The guardrail is the connector's, and `force` is the operator's
//
// Nothing here checks whether a port carries the administration session — the
// API layer cannot know, and the connector can. What this layer owes the
// connector is an HONEST `force`: passed through untouched, never defaulted,
// never set on behalf of somebody who was not shown what they are about to cut.
// A caller that always sent force:true would make the confirmation dialog
// decorative.

// handleApplyPortAdmin shuts and un-shuts ports, NOW.
//
// Body: `{ "ports": { "GE5": false }, "force": false }`, where the bool is
// ENABLED — `false` SHUTS the port. The polarity is stated here, in the
// contract, and in the frontend, because it is the one thing that inverts
// silently: `{"GE5": true}` read as "disable GE5" cuts a customer with a
// request that looks like it turned something on.
//
// site_admin, like every other write that reaches hardware, and for the
// heaviest reason in this package: on the Instant On and SwOS ranges, which
// have neither SSH nor a serial port, shutting the jack that carries the
// administration session ends the conversation permanently and the recourse is
// a paperclip on site. The connector refuses that case unless `force` says the
// operator accepts the trip — see driver.PortAdminPlan.Force.
//
// WHAT IS NOT DONE HERE: no confirmation, no dry run, no per-port capability
// check. A port the platform will not shut comes back in ApplyResult.Skipped
// with its reason — that is an ORDINARY refusal, not an error, and a caller
// receiving an empty Skipped is entitled to believe every named port moved.
func (s *Server) handleApplyPortAdmin(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		Ports map[string]bool `json:"ports"`
		Force bool            `json:"force"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Ports) == 0 {
		respondErr(w, http.StatusBadRequest, "no ports in request")
		return
	}

	var conn, connErr = s.connectorFor(r.Context(), d)
	if connErr != nil {
		respondErr(w, http.StatusUnprocessableEntity, connErr.Error())
		return
	}
	if !conn.WritesExtra("port_admin") {
		respondErr(w, http.StatusUnprocessableEntity,
			"this platform does not declare port administrative state as writable")
		return
	}

	var plan driver.PortAdminPlan
	// Force rides straight through, unexamined. The decision it lifts belongs to
	// the connector; re-deciding it here would put the safeguard in two places
	// and guarantee that one day they disagree.
	plan.Force = in.Force
	var name string
	var enabled bool
	for name, enabled = range in.Ports {
		plan.Ports = append(plan.Ports,
			driver.PortAdminIntent{Port: name, Enabled: enabled})
	}
	sort.Slice(plan.Ports, func(i, j int) bool {
		return plan.Ports[i].Port < plan.Ports[j].Port
	})

	var _, adm, _, err = s.adminFor(r.Context(), d)
	if adm != nil {
		defer adm.Close()
	}
	if err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var applier, isApplier = driver.AsPortAdminApplier(adm)
	if !isApplier {
		respondErr(w, http.StatusUnprocessableEntity,
			"connector declares port administrative state writable but Admin has no ApplyPortAdmin")
		return
	}
	var res driver.ApplyResult
	res, err = applier.ApplyPortAdmin(r.Context(), plan)
	if err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}

	// THE JOURNAL LINE IS BUILT FROM WHAT WAS WRITTEN, NEVER FROM WHAT WAS ASKED.
	//
	// It used to render `in.Ports` — the REQUEST — and that produced a false
	// history, observed on hardware on 2026-08-11: a plan naming core01's three
	// live uplinks was refused in full by the connector's management-VLAN
	// guardrail (empty `ports_written`, three lines in `Skipped`, no command
	// sent), and the fleet log recorded "Port administrative state written on
	// "core01.lab": ET34 → shut, ET37 → shut, ET48 → shut" at WARNING.
	// Somebody reading that history to find when service stopped would have
	// concluded the fleet core had been cut. The same wording also covered the
	// ordinary "already where the plan wants it" case, where the connector sends
	// nothing on purpose.
	//
	// `res.PortsWritten` is the connector's own answer to "which ports moved" —
	// filled from its verification re-read (cf. driver.ApplyResult) — so the line
	// says what happened and the severity follows the ports that ACTUALLY moved:
	// a refused shutdown is not a warning, it is a non-event with a reason.
	var msg, sev = portAdminJournalLine(d.Name, in.Ports, res)
	s.emitDeviceEvent(r, d, "device.port_admin", sev, msg)
	respond(w, http.StatusOK, map[string]any{
		"ok":     true,
		"result": res,
		"steps":  transcriptOf(adm),
	})
}

// maxPowerCycleOffSeconds caps how long an operator may leave power down.
//
// SIXTY SECONDS, and the ceiling exists for a typo, not for a hostile caller:
// `off_for_seconds: 40` with one extra digit is 400 seconds, and a building's
// cameras stay dark for nearly seven minutes because a form field accepted what
// was typed. Nothing legitimate needs longer than a minute — the point of the
// cut is that the powered device's supply collapses, which takes seconds (cf.
// driver.DefaultPowerCycleOff and why it is four) — so the value above the
// ceiling is refused with a 400 rather than clamped. A silently clamped
// duration would teach the operator that the field does nothing.
const maxPowerCycleOffSeconds = 60

// powerCycleHeadroom is what the request context gets ON TOP of the off
// duration: the connector must read the port's configured class, cut, wait,
// and restore it, and only the waiting part is known in advance.
const powerCycleHeadroom = 60 * time.Second

// handlePortPowerCycle cuts PoE on named ports, waits, and restores it, so the
// powered devices reboot.
//
// Body: `{ "ports": ["GE5"], "off_for_seconds": 4 }`. Absent or 0 means
// driver.DefaultPowerCycleOff; above maxPowerCycleOffSeconds is a 400.
//
// # THIS HANDLER BLOCKS FOR THE WHOLE OFF-DURATION
//
// It is a synchronous gesture on purpose: the RESTORE is the contract (cf.
// driver.PortPowerCycler), and a caller that got its answer before the restore
// happened would have no way to learn that the second half failed — i.e. that a
// port is dark. So the response arrives after power is back, and the frontend
// must size its own timeout for `off_for_seconds` plus the round trip, not for
// an ordinary API call.
//
// TWO CONSEQUENCES ARE HANDLED HERE, and they are the reason this endpoint
// looks different from its three siblings:
//
//   - the context must OUTLIVE the wait. r.Context() alone would be a bounded
//     promise the moment anything upstream shortens it, and a deadline that
//     expires mid-cycle strands the port with its power off. The context below
//     is therefore given the off duration plus real headroom;
//   - the context must SURVIVE THE CLIENT GOING AWAY. r.Context() is cancelled
//     when the browser tab closes or the operator hits reload — and cancelling
//     a power cycle halfway through does not undo it, it abandons it, leaving
//     the volts off with nobody watching. context.WithoutCancel is what turns
//     an interrupted request into a completed gesture whose answer nobody
//     reads, which is the strictly better of the two failures.
//
// The controller sets no http.Server WriteTimeout (cf. cmd/controller/main.go),
// so nothing above this handler cuts the response short. If one is ever added,
// it must exceed maxPowerCycleOffSeconds plus powerCycleHeadroom or this
// endpoint becomes a way to leave ports dark.
func (s *Server) handlePortPowerCycle(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		Ports []string `json:"ports"`
		// OffForSeconds: whole seconds. Not a duration string — a JSON body
		// carrying "4s" would be one more thing for a frontend to get subtly
		// wrong on a gesture that reboots hardware.
		OffForSeconds int `json:"off_for_seconds"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Ports) == 0 {
		respondErr(w, http.StatusBadRequest, "no ports in request")
		return
	}
	if in.OffForSeconds < 0 {
		respondErr(w, http.StatusBadRequest,
			"off_for_seconds cannot be negative")
		return
	}
	if in.OffForSeconds > maxPowerCycleOffSeconds {
		respondErr(w, http.StatusBadRequest,
			"off_for_seconds is capped at "+strconv.Itoa(maxPowerCycleOffSeconds)+
				" s: a longer cut leaves the powered devices down for no benefit "+
				"— the supply of a PD collapses in seconds, and an extra digit "+
				"typed here is how a building's cameras stay dark")
		return
	}
	var offFor = time.Duration(in.OffForSeconds) * time.Second
	if offFor == 0 {
		// Zero is a legitimate "you decide": the plan carries it as-is and the
		// connector substitutes DefaultPowerCycleOff. Resolving it here as well
		// would duplicate the default in two packages.
		offFor = driver.DefaultPowerCycleOff
	}

	var conn, connErr = s.connectorFor(r.Context(), d)
	if connErr != nil {
		respondErr(w, http.StatusUnprocessableEntity, connErr.Error())
		return
	}
	// The gate is `port_power_cycle`, NOT `poe`, and the difference is not
	// pedantry: three connectors declare `poe` and exactly one implements
	// PortPowerCycler, so gating on `poe` put a power-cycle button on two
	// platforms that could only refuse it — the "button that leads nowhere"
	// connector.go's mandatory/optional boundary exists to prevent. Knowing how
	// to set a class does not promise the restore half, and half a power cycle is
	// an outage (cf. driver/portadmin.go, § on the name).
	if !conn.WritesExtra("port_power_cycle") {
		respondErr(w, http.StatusUnprocessableEntity,
			"this platform does not declare the PoE power cycle as writable: "+
				"setting a PoE class and promising to put it back are two different "+
				"capabilities, and only the second makes a power cycle safe to offer")
		return
	}

	// Everything from here on runs on the detached context (see the header
	// comment): the authentication, the cut, the wait, and the restore are ONE
	// gesture, and opening the access under a context that can die mid-wait
	// would reintroduce the failure the detachment exists to remove.
	var ctx, cancel = context.WithTimeout(
		context.WithoutCancel(r.Context()), offFor+powerCycleHeadroom)
	defer cancel()

	var _, adm, _, err = s.adminFor(ctx, d)
	if adm != nil {
		defer adm.Close()
	}
	if err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var cycler, isCycler = driver.AsPortPowerCycler(adm)
	if !isCycler {
		respondErr(w, http.StatusUnprocessableEntity,
			"connector declares PoE writable but Admin cannot power-cycle a port: "+
				"knowing how to set a class does not promise the restore half, "+
				"and half a power cycle is an outage")
		return
	}
	var plan = driver.PortCyclePlan{
		Ports:  append([]string{}, in.Ports...),
		OffFor: offFor,
	}
	sort.Strings(plan.Ports)
	var res driver.ApplyResult
	res, err = cycler.CyclePortPower(ctx, plan)
	if err != nil {
		// The transcript matters MORE than usual on this path: a failed restore
		// names, in ApplyResult.Notes, the class it could not put back — i.e.
		// which port is dark and what it should be carrying. Returning only the
		// error string would drop exactly that.
		respond(w, http.StatusBadGateway, map[string]any{
			"error":  err.Error(),
			"result": res,
			"steps":  transcriptOf(adm),
		})
		return
	}

	// SevWarning unconditionally: every device on these ports has just rebooted.
	// There is no "harmless" variant of this gesture to grade it against.
	s.emitDeviceEvent(r, d, "device.port_power_cycle", model.SevWarning,
		"PoE power-cycled on \""+d.Name+"\" for "+
			strconv.Itoa(int(offFor/time.Second))+" s: "+
			joinPortNames(plan.Ports)+" — the powered devices rebooted")
	respond(w, http.StatusOK, map[string]any{
		"ok":     true,
		"result": res,
		"steps":  transcriptOf(adm),
	})
}

// portAdminSummary names what was asked, for the event log: "GE5 → shut, GE6 →
// enabled". Sorted, and written in verbs rather than "true" / "false" — a
// history line read six months later must not require remembering which way the
// bool pointed.
func portAdminSummary(ports map[string]bool) string {
	var names = make([]string, 0, len(ports))
	var name string
	for name = range ports {
		names = append(names, name)
	}
	sort.Strings(names)
	var out = ""
	for _, name = range names {
		if out != "" {
			out += ", "
		}
		out += name + " → " + adminWord(ports[name])
	}
	return out
}

// portAdminJournalLine builds the fleet-log line and its severity from what the
// connector actually wrote.
//
// PURE, and separated from the handler for the reason the defect above gives:
// the whole question is whether the history describes the DEVICE or the
// REQUEST, and that is provable without a Store, a session or a switch.
func portAdminJournalLine(deviceName string, requested map[string]bool,
	res driver.ApplyResult) (string, int) {

	var moved = map[string]bool{}
	var name string
	for _, name = range res.PortsWritten {
		moved[name] = requested[name]
	}
	if len(moved) == 0 {
		return "NO port administrative state changed on \"" + deviceName + "\": " +
			portAdminSummary(requested) + " was requested and the connector wrote nothing" +
			portAdminRefusalTail(res), model.SevInfo
	}
	var sev = model.SevInfo
	var enabled bool
	for _, enabled = range moved {
		if !enabled {
			// Shutting a port can cut a customer, and that line must stand out in
			// a history read after the fact by somebody looking for when service
			// stopped. Enabling only is ordinary.
			sev = model.SevWarning
			break
		}
	}
	return "Port administrative state written on \"" + deviceName + "\": " +
		portAdminSummary(moved) + portAdminRefusalTail(res), sev
}

// portAdminRefusalTail names the refusals on the journal line.
//
// The COUNT and not the sentences: a connector's refusal is several lines long
// (the management-VLAN guardrail alone is four), and a fleet history is read by
// scanning. The reasons are in the response the operator is looking at when they
// act; what the history owes a reader six months later is that ports were named
// and not moved, so they go looking.
func portAdminRefusalTail(res driver.ApplyResult) string {
	var n = len(res.AllNotApplied())
	if n == 0 {
		return ""
	}
	return " (" + strconv.Itoa(n) + " refused by the connector)"
}

func adminWord(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "shut"
}

// joinPortNames renders a port list for an event line.
func joinPortNames(ports []string) string {
	var out = ""
	var name string
	for _, name = range ports {
		if out != "" {
			out += ", "
		}
		out += name
	}
	return out
}

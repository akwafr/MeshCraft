package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/store"
)

// Pipelines: what meshcraft does on the fleet, and what gets stuck.
// See docs/01-Architecture/Unified monitoring and pipelines §5.

// stepsFrom converts a connector access transcript into pipeline steps.
//
// The transcript already existed and was only returned in an HTTP response:
// it vanished as soon as the page closed. Attaching it to a pipeline makes it
// consultable later, which is precisely when it is needed.
//
// [driver.Step] rather than a connector-specific type: all three carried an
// identical copy, and three identical definitions by convention hold until one
// gains a field — on that day, silent conversion loses it without any test
// noticing.
func stepsFrom(steps []driver.Step) []store.PipelineStep {
	var out = make([]store.PipelineStep, 0, len(steps))
	var i int
	for i = range steps {
		var state = "done"
		if !steps[i].OK {
			state = "failed"
		}
		out = append(out, store.PipelineStep{
			Seq: i + 1, Name: steps[i].What, State: state, Detail: steps[i].Detail,
		})
	}
	return out
}

// skippedSteps adds what a safeguard refused to do.
//
// Neither success nor failure: a DECISION. Bucketing it into either would
// make it invisible — exactly what made the safeguard look like a bug.
func skippedSteps(from int, skipped []string) []store.PipelineStep {
	var out []store.PipelineStep
	var i int
	for i = range skipped {
		out = append(out, store.PipelineStep{
			Seq: from + i + 1, Name: "spared by safeguard",
			State: "skipped", Detail: skipped[i],
		})
	}
	return out
}

// unsupportedSteps records what the PLATFORM cannot write.
//
// Bucketed as "skipped" like spared ports — the UI only knows three states —
// but under a name that does not confuse: a spared port can be forced, an
// unsupported intent must be implemented. And unlike spared ports, these steps
// do NOT turn the apply into "blocked": there is nothing to arbitrate, and
// retrying would change nothing.
func unsupportedSteps(from int, notes []string) []store.PipelineStep {
	var out []store.PipelineStep
	var i int
	for i = range notes {
		out = append(out, store.PipelineStep{
			Seq: from + i + 1, Name: "not applied: beyond connector capabilities",
			State: "skipped", Detail: notes[i],
		})
	}
	return out
}

// noteSteps records apply facts that need saying and no decision.
//
// Same bucket as unsupportedSteps: visible in the transcript, never "blocked".
// Example: unresolved « all » while the site plan is empty — apply does not strip
// tagged membership; Force would change nothing.
func noteSteps(from int, notes []string) []store.PipelineStep {
	var out []store.PipelineStep
	var i int
	for i = range notes {
		out = append(out, store.PipelineStep{
			Seq: from + i + 1, Name: "note",
			State: "skipped", Detail: notes[i],
		})
	}
	return out
}

// recordPipeline records an operation without ever failing the caller.
//
// The trace is valuable, but it comes AFTER the action: a successful apply that
// does not get recorded remains a successful apply, and refusing the request
// for that would be disproportionate.
func (s *Server) recordPipeline(ctx context.Context, p store.Pipeline, by string) {
	var _, err = s.Store.RecordPipeline(ctx, p, by)
	if err != nil {
		return
	}
}

// pipelineForApply builds the pipeline for a configuration apply.
func pipelineForApply(d model.Device, out applyOutcome, failure error) store.Pipeline {
	var steps = append(stepsFrom(out.Steps),
		skippedSteps(len(out.Steps), out.Result.Skipped)...)
	steps = append(steps, unsupportedSteps(len(steps), out.Unsupported)...)
	// Result.Notes (floors…) plus re-read notes: both are facts, neither blocks.
	steps = append(steps, noteSteps(len(steps), out.Notes)...)
	var p = store.Pipeline{
		SiteID: d.SiteID, Kind: "config", DeviceID: &d.ID, Steps: steps,
	}
	switch {
	case failure != nil:
		p.State = store.PipelineFailed
		p.Label = "Configuration apply on \"" + d.Name + "\""
		p.BlockedReason = failure.Error()
	case len(out.Result.Skipped) > 0 || len(out.Remaining) > 0:
		// An apply that leaves gaps is not a failure: it is BLOCKED on something
		// that must be arbitrated. Bucketing it as failure would retry an
		// operation that would do exactly the same thing.
		p.State = store.PipelineBlocked
		p.Label = "Configuration partially applied on \"" + d.Name + "\""
		p.BlockedReason = blockedApplyReason(out)
	default:
		p.State = store.PipelineDone
		p.Label = "Configuration applied on \"" + d.Name + "\" (" +
			strconv.Itoa(len(out.Result.PortsWritten)) + " port(s))"
	}
	return p
}

// blockedApplyReason phrases why an apply stayed partial — without the absurd
// "0 gap(s) remain: …" when only a safeguard skipped ports.
func blockedApplyReason(out applyOutcome) string {
	var parts []string
	var n = len(out.Remaining)
	if n > 0 {
		parts = append(parts, strconv.Itoa(n)+" gap(s) remain")
	}
	if len(out.Result.Skipped) > 0 {
		parts = append(parts, strings.Join(out.Result.Skipped, " "))
	}
	return strings.Join(parts, ": ")
}

func (s *Server) handleListPipelines(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "operator")
	if !ok {
		return
	}
	var limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	var list, err = s.Store.ListPipelines(r.Context(), siteID, limit)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"pipelines": list})
}

func (s *Server) handleGetPipeline(w http.ResponseWriter, r *http.Request) {
	var p, err = s.Store.GetPipeline(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	// A pipeline belongs to a site: without this check, guessing a UUID would
	// expose the transcript of a fleet you do not administer.
	if !s.Auth.CanSite(r.Context(), p.SiteID, "operator") {
		respondErr(w, http.StatusForbidden, "insufficient rights on this site")
		return
	}
	respond(w, http.StatusOK, p)
}

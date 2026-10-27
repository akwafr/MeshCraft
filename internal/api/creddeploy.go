package api

// FLEET-WIDE CREDENTIAL DEPLOYMENT — the half of "rotate" that touches the fleet.
//
// # The defect this closes
//
// Rotating a site credential stores a new generation and marks the previous one
// `retiring`. NOTHING pushed it to equipment. Deployment was a per-device gesture
// (`POST /api/devices/{id}/credentials/apply`), so an operator who rotated a
// site-wide password saw the vault change and the fleet not change — observed on
// lab/CaserneLab 2026-08-07, and confirmed by reading back the head secret and
// finding the switch still accepting the previous one. The first fleet rotation was
// therefore done BY HAND on 2026-08-08, eight devices, one endpoint call each.
//
// That hand run is the specification of this file, including its one failure: on
// `core01` the push refused CLEANLY, before writing anything, and the per-step
// transcript is what made it diagnosable in minutes. Everything below is built so
// the automatic run cannot be less careful than the human one was.
//
// # What it is, and what it deliberately is NOT
//
// It is an OPERATOR-TRIGGERED run, not a background loop. Every other controller-side
// sweep in this package (RunPoESampler, RunConfigPoll, RunSNMPHeal) reads; this one
// changes the only door into a switch. A ticker that pushes secrets to a fleet on its
// own is a machine that can lock an operator out of a site while nobody is watching,
// and no cadence makes that acceptable. So it starts when somebody asks, it reports
// what it did, and it stops.
//
// It also does NOT mint, promote or retire a generation. [store.PlanDeployment] and
// [store.DeploymentPlan.SafeToPromote] hold that gate and the header of
// internal/store/deployment.go argues the ordering; this file is step 2 of that
// ordering — "push it device by device, RECORDING PER DEVICE which generation it now
// carries" — and nothing else. Deploying the generation the vault already made active
// is what keeps this change off the live apply path: `candidatesFor` aims at the first
// `active` generation, this run deploys exactly that one, so the try order validated by
// hand over eight devices is the one the run uses.
//
// # Resumability is a property of the database, not of this object
//
// PARTIAL FAILURE IS THE NORMAL CASE. A fleet always has a switch that is off, a proxy
// that is down, a device whose connector declares no writable account. So a run is not
// a transaction to be recovered: progress lives in `device_credential_state`, written
// device by device as each one CONFIRMS, and resuming is simply starting again. The
// second run re-reads observed state, finds the devices that took the secret already
// carrying it, and skips them — see [credDeployEligible]. A run object that vanished
// with a controller restart costs nothing.
//
// # The four rules that keep this from burning the fleet
//
//  1. ONE DEVICE AT A TIME, ONE RUN AT A TIME. Session budgets are small and documented
//     in blood: Instant On grants five, a saturated ProCurve 2610 authenticates and then
//     refuses the channel. The run is sequential, it consults the apply queue's own
//     serialization (applyQueuedOrRunning) and SKIPS a device an operator's gesture is
//     already using, and [credDeployRegistry] refuses a second concurrent run — two runs
//     would double every number above while each believed itself well-behaved;
//  2. IT NEVER NAMES AN ACCOUNT. A deployment changes the SECRET of the account already
//     there. The rename sequence (create, verify, delete) is what produced the defect
//     that motivated this section, and this path does not contain it: it never calls
//     [driver.AccountManager], and [credDeployAccount] refuses the device outright when
//     the account the session entered through is not the one on record;
//  3. IT ONLY TOUCHES DEVICES IT HAS ALREADY BEEN INSIDE. A device with no confirmed
//     installation under this credential is left to the per-device gesture, where an
//     operator is present and can supply the current secret. See the paragraph on the
//     CSS326 in [credDeployEligible] — that rule is the one standing between an
//     unattended sweep and a site visit with a paperclip;
//  4. AN UNCONFIRMED WRITE STOPS THE RUN. Not the device: the run. See [credDeployHalts].
//
// # It cannot promote anything, so it cannot strand anything
//
// The previous generation stays `retiring`, which is still a candidate on every login
// (`SecretsForCredential`), for as long as any device is recorded as carrying it. This
// run only ever moves devices FORWARD onto a generation the vault already holds, and it
// records that move only after reading it back. A device it fails on keeps the secret it
// had, keeps working, and appears in the outstanding list of
// `GET /credentials/{id}/deployment`.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/auth"
	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/pwpolicy"
	"meshcraft/internal/store"
)

const (
	// credDeployDeviceBudget caps ONE device's sessions and write. Three minutes is
	// the autoapply ceiling, and this gesture belongs to that family rather than to
	// the two-minute read family (poeSampleDeviceBudget, configPollDeviceBudget): it
	// opens a session, writes, and opens a SECOND session to read the result back.
	// It exists so one wedged transport cannot hold the whole run.
	credDeployDeviceBudget = 3 * time.Minute
	// credDeployPause: the gap between two devices.
	//
	// Not about the device just left — it is closed — but about everything shared:
	// the proxy tunnel every session in a site rides through, and the operator whose
	// own gesture must be able to get a slot in a run that may last twenty minutes.
	// Three seconds costs a minute on a fleet of twenty and buys a window that is
	// wide enough to matter on a CLI platform.
	credDeployPause = 3 * time.Second
	// credDeployMaxConsecutiveFailures trips the run when devices refuse in a row.
	//
	// THREE IN A ROW IS NOT THREE COINCIDENCES. A proxy that lost its tunnel, a
	// controller that cannot decrypt the vault, a credential resolved against the
	// wrong site: each of those refuses on every device, and none of them is fixed by
	// trying the fourth. Continuing spends the FLEET's authentication budget on a
	// fault that is not the fleet's — and on Instant On, whose firmware locks an
	// account out (cf. triesPerAccount), spending it is not free.
	credDeployMaxConsecutiveFailures = 3
)

// Outcomes, one per device, as they appear in the run report.
const (
	// credDeployInstalled: the secret was written AND read back under it.
	credDeployInstalled = "installed"
	// credDeployRehearsed: a rehearsal (`write` false) got in, found the account on
	// record, and stopped before writing.
	credDeployRehearsed = "rehearsed"
	// credDeploySkipped: nothing was attempted on the device. Always carries a reason.
	credDeploySkipped = "skipped"
	// credDeployFailed: the attempt did not reach a write — no session, no connector,
	// a refusal before send. The device carries exactly what it carried before.
	credDeployFailed = "failed"
	// credDeployUnconfirmed: THE DANGEROUS ONE. A write was accepted and the read-back
	// did not open the device. Halts the run — see [credDeployHalts].
	credDeployUnconfirmed = "unconfirmed"
)

// credDeployTarget is everything the eligibility rule needs to know about one device,
// read from the database so that the rule itself stays pure.
type credDeployTarget struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	// Status is `devices.status`: ok | off | unknown (cf. reachability.go).
	Status string `json:"status"`
	MgmtIP string `json:"mgmt_ip"`
	// InstalledGeneration / InstalledUsername: what the device is OBSERVED to carry
	// under this credential. Zero and empty mean "never confirmed", which is a
	// refusal here rather than a starting point.
	InstalledGeneration int    `json:"installed_generation"`
	InstalledUsername   string `json:"installed_username"`
}

// credDeploySkip is a device the run will not touch, and why.
type credDeploySkip struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	Reason     string `json:"reason"`
}

// credDeployResult is what happened on one device.
type credDeployResult struct {
	DeviceID   string    `json:"device_id"`
	DeviceName string    `json:"device_name"`
	Outcome    string    `json:"outcome"`
	Detail     string    `json:"detail,omitempty"`
	Account    string    `json:"account,omitempty"`
	Generation int       `json:"generation,omitempty"`
	At         time.Time `json:"at"`
	// Steps is the connector's own transcript. Kept per device for the reason the
	// hand run proved: `core01` refused cleanly and the per-step record is what made
	// the cause obvious in minutes instead of a site visit. A run that summarised its
	// devices to "3 ok, 1 failed" would have lost exactly that.
	Steps []driver.Step `json:"steps,omitempty"`
}

// credDeployEligible decides whether the run may touch THIS device.
//
// PURE, for the reason poeSampleWanted and configPollWanted are pure and then some:
// this rule spends the fleet's session budget, and the devices it lets through are the
// ones whose only door it is about to change. It must be provable without a database
// and without a device.
//
// The refusals, each one earned:
//
//   - NO MANAGEMENT ADDRESS: there is nothing to dial;
//   - NOT VISIBLE. `status` must be "ok". A device meshcraft cannot currently see does
//     NOT count as online, and "unknown" means precisely that nobody who can see it is
//     connected (cf. judgeReach). The read loops tolerate "unknown" and try anyway,
//     because a wasted read costs a timeout; this is a WRITE, and the difference
//     between "the device refused" and "we never reached it" has to be established
//     before the write, not guessed after it;
//   - NEVER CONFIRMED UNDER THIS CREDENTIAL. This is the rule that keeps an unattended
//     sweep away from the class of mistake that is not recoverable. The lab CSS326 has
//     no SSH, no Telnet and no serial port: its front-panel Reset button is the ONLY
//     recourse after a lost password, and it requires somebody standing in the rack.
//     On such a device a push is safe exactly when meshcraft already knows which secret
//     and which account open it — which is what a confirmed `installed_generation` and
//     a recorded `installed_username` mean. Without them the run would be guessing, and
//     a guess made by a machine at 02:00 with nobody watching is the worst version of
//     that guess. The per-device gesture is where an unknown device belongs: an operator
//     is present, can supply `current_secret`, and can rehearse with `dry_run`. Such a
//     device is not silently dropped — it stays in the outstanding list and keeps
//     blocking promotion, which is the honest report;
//   - ALREADY CARRYING THE TARGET: this is what makes a re-run a resume rather than a
//     second push at everything;
//   - AHEAD OF THE TARGET: a device on a newer generation is a run that happened out of
//     order. Pushing an older secret onto it would be a downgrade this run has no
//     mandate for, and it would be recorded as progress.
func credDeployEligible(t credDeployTarget, target int) (bool, string) {
	if target <= 0 {
		return false, "no active generation to deploy for this credential"
	}
	if t.MgmtIP == "" {
		return false, "no known management address"
	}
	if t.Status != "ok" {
		return false, "meshcraft cannot currently see this device (status \"" +
			t.Status + "\"): a device nobody can reach is not a device to push a " +
			"password to, and it stays in the outstanding list until it is back"
	}
	if t.InstalledGeneration <= 0 || t.InstalledUsername == "" {
		return false, "no secret of this credential has ever been CONFIRMED on this " +
			"device: a fleet run only changes secrets it can already get in with. " +
			"Use the per-device gesture, where the current password can be supplied " +
			"and the attempt rehearsed"
	}
	if t.InstalledGeneration == target {
		return false, "already carries generation " + itoa(target)
	}
	if t.InstalledGeneration > target {
		return false, "carries generation " + itoa(t.InstalledGeneration) +
			", which is NEWER than the one being deployed: pushing here would be a " +
			"downgrade recorded as progress"
	}
	return true, ""
}

// credDeployQueue builds the run's work list, furthest behind first.
//
// PURE, and separate from the pass for configPollOrder's reason plus one of its own:
// ORDER IS THE ONLY PROTECTION A RUN THAT CANNOT FINISH HAS. A run gets interrupted —
// the controller restarts, the operator stops it, three devices in a row refuse and it
// trips. Whatever it had time for should be the devices whose remaining margin is
// smallest, and that is the ones furthest behind: their generation is the one closest
// to having no holders left, i.e. closest to being purged out from under them
// (`RetirableGenerations`). Ties break on name, then id, so a run is reproducible and a
// test can assert an order.
//
// The second return is not a leftover: every device the run declines is REPORTED with
// its reason. A fleet-wide gesture that silently omitted devices would put the operator
// back in front of the surprise this whole section exists to end.
func credDeployQueue(target int, in []credDeployTarget) ([]credDeployTarget, []credDeploySkip) {
	var queue = []credDeployTarget{}
	var skipped = []credDeploySkip{}
	var t credDeployTarget
	for _, t = range in {
		var ok, why = credDeployEligible(t, target)
		if !ok {
			skipped = append(skipped, credDeploySkip{
				DeviceID: t.DeviceID, DeviceName: t.DeviceName, Reason: why})
			continue
		}
		queue = append(queue, t)
	}
	sort.SliceStable(queue, func(i, j int) bool {
		if queue[i].InstalledGeneration != queue[j].InstalledGeneration {
			return queue[i].InstalledGeneration < queue[j].InstalledGeneration
		}
		if queue[i].DeviceName != queue[j].DeviceName {
			return queue[i].DeviceName < queue[j].DeviceName
		}
		return queue[i].DeviceID < queue[j].DeviceID
	})
	return queue, skipped
}

// credDeployAccount decides which account the run changes the secret of — and that is
// the ONLY account decision this whole path makes.
//
// THE RUN NEVER NAMES AN ACCOUNT. It changes the secret of the account the session
// entered through, having first checked that this is the account meshcraft has on
// record for this device. The username and the secret are separate decisions, and
// conflating them is what produced the defect that motivated this section: a create
// colliding with an existing credential silently rewrote `mcadmin` to `admin`.
//
// `recorded` is `device_credential_state.installed_username` — what the device is
// OBSERVED to carry — and NOT `site_credentials.username`. The two disagree in the
// field and the disagreement is legitimate: on the lab CSS326 the credential says
// `mcadmin` while the installed account is `admin`, because SwOS has one nameless
// account it cannot rename. Any deployment logic keying on the credential's username
// would try to create `mcadmin` there, which is the rename this run must never perform.
//
// A mismatch is a REFUSAL, not a repair. It means the device answered to a name other
// than the one on record — a half-finished rename, a manual change, a device that is
// not the one meshcraft thinks it is — and every one of those needs a human looking at
// the transcript, not an unattended write.
//
// Pure: it is the last gate before a write, so it must be readable and testable on its
// own.
func credDeployAccount(entered, recorded string) (string, error) {
	if entered == "" {
		return "", errors.New("the connector did not report which account opened the " +
			"session: a fleet run will not write a secret without knowing whose it is")
	}
	if recorded == "" {
		return "", errors.New("no account is on record for this device under this " +
			"credential: nothing to change the secret of")
	}
	if !strings.EqualFold(entered, recorded) {
		return "", errors.New("the session entered as \"" + entered + "\" but the " +
			"account on record is \"" + recorded + "\": a fleet run changes the SECRET " +
			"of the account already there, it never creates, renames or deletes one. " +
			"Settle which account this device carries with the per-device gesture")
	}
	// The device's OWN spelling wins: it is the case the device answered to, and it is
	// what will be recorded as installed.
	return entered, nil
}

// credDeployConfirm checks the generation the operator believes they are deploying
// against the one the vault actually makes active.
//
// A rotation between opening the screen and pressing the button is not exotic — it is
// two operators, or one operator and a script. Without this the run would push a
// generation nobody asked for, and would record it as success. Pure, and the message
// says both numbers because the operator's next move depends on which one surprised
// them.
func credDeployConfirm(requested, target int) error {
	if target <= 0 {
		return errors.New("this credential has no active generation to deploy")
	}
	if requested <= 0 {
		return errors.New("name the generation to deploy (\"generation\": " +
			itoa(target) + "): a fleet-wide secret push does not start on an implicit target")
	}
	if requested != target {
		return errors.New("generation " + itoa(requested) + " was requested but the " +
			"active generation is " + itoa(target) + ": the credential rotated since this " +
			"screen was opened. Re-read the deployment view before pushing")
	}
	return nil
}

// credDeployHalts says whether one device's outcome must stop the whole run.
//
// ONLY AN UNCONFIRMED WRITE HALTS, and the distinction is the point.
//
// A device that refused a session, has no connector, or whose secret the platform
// rejects BEFORE send has not been changed: the run moves on, and the device appears in
// the outstanding list. Stopping there would make the run useless — partial failure is
// the normal case, and a fleet always has one of those.
//
// An UNCONFIRMED write is the opposite kind of event. The device accepted the form and
// then did not open under the new secret — a failure mode observed on Instant On, where
// an accepted form had no effect. Whatever caused it is a property of the platform or
// of the connector, not of the device, so the next unit of the same range will do
// exactly the same thing. Continuing would walk the fleet into that state one device at
// a time, and on a range with no out-of-band access each of those is a site visit. The
// run stops, the transcript is on the run, and a human decides.
func credDeployHalts(outcome string) bool { return outcome == credDeployUnconfirmed }

// credDeployTripped stops a run whose failures are systemic.
//
// Consecutive, and reset by any device that gets through: three refusals scattered
// across a fleet of forty are three switches that are off, which is the normal case.
// Three in a row are one cause, and it is on our side of the wire.
func credDeployTripped(consecutiveFailures int) bool {
	return consecutiveFailures >= credDeployMaxConsecutiveFailures
}

// credDeployRun is one operator's run over one credential.
type credDeployRun struct {
	mu sync.Mutex

	CredentialID string
	SiteID       string
	Label        string
	Target       int
	// Write false is a REHEARSAL: sessions are opened and every gate is applied, and
	// nothing is written. See [credDeployInput].
	Write     bool
	Actor     string
	StartedAt time.Time

	queued   []credDeployTarget
	skipped  []credDeploySkip
	results  []credDeployResult
	finished *time.Time
	// stopped explains an early end — cancelled, tripped, halted. Empty when the run
	// walked its whole queue.
	stopped string
	cancel  context.CancelFunc
}

// credDeployStatus is the run as the API serves it.
type credDeployStatus struct {
	CredentialID string             `json:"credential_id"`
	Label        string             `json:"label"`
	Target       int                `json:"target"`
	Write        bool               `json:"write"`
	Actor        string             `json:"actor"`
	StartedAt    time.Time          `json:"started_at"`
	FinishedAt   *time.Time         `json:"finished_at,omitempty"`
	Running      bool               `json:"running"`
	Stopped      string             `json:"stopped,omitempty"`
	Queued       []credDeployTarget `json:"queued"`
	Skipped      []credDeploySkip   `json:"skipped"`
	Results      []credDeployResult `json:"results"`
	Remaining    int                `json:"remaining"`
}

func (run *credDeployRun) snapshot() credDeployStatus {
	run.mu.Lock()
	defer run.mu.Unlock()
	var st = credDeployStatus{
		CredentialID: run.CredentialID,
		Label:        run.Label,
		Target:       run.Target,
		Write:        run.Write,
		Actor:        run.Actor,
		StartedAt:    run.StartedAt,
		FinishedAt:   run.finished,
		Running:      run.finished == nil,
		Stopped:      run.stopped,
		Queued:       append([]credDeployTarget{}, run.queued...),
		Skipped:      append([]credDeploySkip{}, run.skipped...),
		Results:      append([]credDeployResult{}, run.results...),
	}
	st.Remaining = len(run.queued) - len(run.results)
	if st.Remaining < 0 {
		st.Remaining = 0
	}
	return st
}

func (run *credDeployRun) record(res credDeployResult) {
	run.mu.Lock()
	defer run.mu.Unlock()
	run.results = append(run.results, res)
}

func (run *credDeployRun) end(why string) {
	run.mu.Lock()
	defer run.mu.Unlock()
	var now = time.Now()
	run.finished = &now
	run.stopped = why
}

// credDeployRegistry holds the run in flight and the last run per credential.
//
// ONE RUN AT A TIME ACROSS THE WHOLE CONTROLLER, and that is not a convenience. Every
// per-device guardrail in this file — sequential, one session, a pause between devices
// — is stated per run. Two runs would double all of them while each one believed itself
// well-behaved, and the fleet's session ceilings are counted per DEVICE by the device,
// not per run by us. Two runs over two different credentials can still land on the same
// switch: a device carries a site credential and a technical-tag one, and both resolve
// through the same five sessions.
//
// In memory only. A run is a gesture somebody is watching, not state to survive a
// restart: what survives is every confirmed installation, in `device_credential_state`,
// which is what makes the next run a resume.
type credDeployRegistry struct {
	mu sync.Mutex
	// active: credential id of the run in flight, empty when none.
	active string
	runs   map[string]*credDeployRun
}

// begin registers a run, or says which one is already in flight.
func (reg *credDeployRegistry) begin(run *credDeployRun) (bool, string) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.active != "" {
		return false, reg.active
	}
	if reg.runs == nil {
		reg.runs = map[string]*credDeployRun{}
	}
	reg.active = run.CredentialID
	reg.runs[run.CredentialID] = run
	return true, ""
}

func (reg *credDeployRegistry) finish(credID string) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.active == credID {
		reg.active = ""
	}
}

func (reg *credDeployRegistry) get(credID string) *credDeployRun {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return reg.runs[credID]
}

// activeSite names the site of the run in flight, empty when none.
//
// EXISTS FOR THE READ LOOPS, not for this file. RunConfigPoll and RunPoESampler
// serialise against applies through applyQueuedOrRunning, and a deployment run
// is invisible to that — so a config poll could open a second session on the
// very switch this run is changing the password of. The fleet's session ceilings
// are counted per DEVICE by the device (Instant On grants five, a saturated
// ProCurve 2610 authenticates then refuses the channel), and the loser of that
// race would be the write, on the one gesture where a half-finished attempt is
// worst.
//
// SITE-GRAINED, not device-grained, and deliberately coarser than it could be.
// The run's queue is known, so a per-device answer is possible — but the queue
// SHRINKS as the run proceeds, so a device could be absent from it at the moment
// the poller asks and be reached a second later. A run is operator-triggered,
// watched and bounded; a config poll skipped during one costs at most one
// cadence, and the loop's own gate is the age of the observation, so a skipped
// device is simply still due on the next pass.
func (reg *credDeployRegistry) activeSite() string {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.active == "" {
		return ""
	}
	var run = reg.runs[reg.active]
	if run == nil {
		return ""
	}
	return run.SiteID
}

// credDeployRunningOn reports whether a fleet credential deployment is in flight
// on this device's site. See [credDeployRegistry.activeSite].
func (s *Server) credDeployRunningOn(siteID string) bool {
	if siteID == "" {
		return false
	}
	return s.credDeploy.activeSite() == siteID
}

// credDeployInput is the start request.
type credDeployInput struct {
	// Generation the operator believes is being deployed. REQUIRED — see
	// [credDeployConfirm].
	Generation int `json:"generation"`
	// Write must be explicitly true for anything to be written to a device.
	//
	// THE DEFAULT IS A REHEARSAL, and the asymmetry is deliberate. A missing field, a
	// hand-written curl, a UI that forgets to send the flag: all of them must produce
	// the harmless run, not the fleet-wide write. A rehearsal is not a formality
	// either — it opens the real session on every device with the real candidate list
	// and applies every gate up to and including [credDeployAccount], which is exactly
	// the class of failure the hand run of 2026-08-08 hit on `core01`. It just stops
	// before the device changes.
	Write bool `json:"write"`
}

// handleStartCredentialDeploy starts a fleet-wide deployment of the credential's active
// generation.
//
// `site_admin`, like the per-device apply it repeats: this writes an administration
// password onto every switch in a scope.
func (s *Server) handleStartCredentialDeploy(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var credID = chi.URLParam(r, "credID")
	var cred, found, err = s.siteCredentialByID(r.Context(), siteID, credID)
	if err != nil {
		fail(w, err)
		return
	}
	if !found {
		respondErr(w, http.StatusNotFound, "not found")
		return
	}
	var in credDeployInput
	if !decode(w, r, &in) {
		return
	}

	// PER-DEVICE CREDENTIALS ARE OUT OF SCOPE, and refusing is the honest answer.
	// Under `per_device` each device has its own generation numbering
	// (`credential_generations.device_id` is set), so "the active generation" is not
	// one number and this run's whole shape — one target, one queue, one gate — does
	// not apply. Pretending otherwise would push one device's secret to its neighbours.
	if cred.PerDevice {
		respondErr(w, http.StatusUnprocessableEntity,
			"this credential holds a DISTINCT secret per device: there is no single "+
				"generation to deploy fleet-wide. Rotate and push these one device at a time")
		return
	}

	var target int
	target, err = s.activeGeneration(r.Context(), credID)
	if err != nil {
		fail(w, err)
		return
	}
	var confirmErr = credDeployConfirm(in.Generation, target)
	if confirmErr != nil {
		respondErr(w, http.StatusConflict, confirmErr.Error())
		return
	}

	var targets []credDeployTarget
	targets, err = s.credDeployTargets(r.Context(), siteID, cred)
	if err != nil {
		fail(w, err)
		return
	}
	var queue, skipped = credDeployQueue(target, targets)

	var me = auth.UserFrom(r.Context())
	var run = &credDeployRun{
		CredentialID: credID,
		SiteID:       siteID,
		Label:        credLabel(cred),
		Target:       target,
		Write:        in.Write,
		Actor:        me.Email,
		StartedAt:    time.Now(),
		queued:       queue,
		skipped:      skipped,
		results:      []credDeployResult{},
	}
	var started, busy = s.credDeploy.begin(run)
	if !started {
		respondErr(w, http.StatusConflict,
			"a credential deployment is already running (credential "+busy+"). "+
				"Runs are sequential on purpose: the switches in this fleet cap "+
				"concurrent sessions at four or five, and two runs would spend that "+
				"budget twice over")
		return
	}

	// THE RUN OUTLIVES THE REQUEST, so it does not inherit the request's context —
	// that one is cancelled the moment we respond, which would kill the run on its
	// first device. Cancellation comes from the operator (DELETE) or from process
	// shutdown, and never from the browser closing a tab.
	var ctx, cancel = context.WithCancel(context.WithoutCancel(r.Context()))
	run.cancel = cancel

	var what = "rehearsal"
	if in.Write {
		what = "WRITE"
	}
	var sid = siteID
	_ = s.Store.InsertEvent(r.Context(), model.Event{
		SiteID: &sid, Type: "credential.deploy_started", Severity: model.SevWarning,
		Message: "Fleet deployment of generation " + itoa(target) + " for \"" +
			run.Label + "\" started (" + what + "): " + itoa(len(queue)) +
			" device(s) queued, " + itoa(len(skipped)) + " skipped",
		Actor: me.Email,
	})
	go s.runCredDeploy(ctx, run)
	respond(w, http.StatusAccepted, run.snapshot())
}

// handleCredentialDeployStatus serves the last run for this credential.
//
// `operator`, like the deployment view it belongs beside: it carries account names,
// generation numbers and transcripts, never a secret.
func (s *Server) handleCredentialDeployStatus(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "operator")
	if !ok {
		return
	}
	var credID = chi.URLParam(r, "credID")
	var _, found, err = s.siteCredentialByID(r.Context(), siteID, credID)
	if err != nil {
		fail(w, err)
		return
	}
	if !found {
		respondErr(w, http.StatusNotFound, "not found")
		return
	}
	var run = s.credDeploy.get(credID)
	if run == nil {
		respondErr(w, http.StatusNotFound, "no deployment has been run for this credential")
		return
	}
	respond(w, http.StatusOK, run.snapshot())
}

// handleStopCredentialDeploy asks the run to stop.
//
// It stops BETWEEN devices, never inside one: a write already sent has to be read back
// and recorded, or the database would lose track of a secret that is on a switch. That
// is the one thing worse than a run that takes twenty seconds too long to stop.
func (s *Server) handleStopCredentialDeploy(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var credID = chi.URLParam(r, "credID")
	var _, found, err = s.siteCredentialByID(r.Context(), siteID, credID)
	if err != nil {
		fail(w, err)
		return
	}
	if !found {
		respondErr(w, http.StatusNotFound, "not found")
		return
	}
	var run = s.credDeploy.get(credID)
	if run == nil || run.cancel == nil {
		respondErr(w, http.StatusNotFound, "no deployment is running for this credential")
		return
	}
	run.cancel()
	respond(w, http.StatusAccepted, map[string]any{
		"stopping": true,
		"note": "the run stops after the device it is on: a write already sent must be " +
			"read back and recorded before anything else happens",
	})
}

// siteCredentialByID finds one of the site's credentials, WITHOUT touching a secret.
//
// Through the site's list rather than by id, for the reason handleCredentialDeployment
// gives: it establishes ownership — a credential id from another site must read as
// "not found" — and a deployment path has no business decrypting anything to check
// whose it is.
func (s *Server) siteCredentialByID(ctx context.Context, siteID,
	credID string) (model.SiteCredential, bool, error) {
	var creds, err = s.Store.ListSiteCredentials(ctx, siteID)
	if err != nil {
		return model.SiteCredential{}, false, err
	}
	var i int
	for i = range creds {
		if creds[i].ID == credID {
			return creds[i], true, nil
		}
	}
	return model.SiteCredential{}, false, nil
}

// activeGeneration is the generation a push actually places: the newest `active`,
// scope-wide one.
//
// Read from the same place `handleCredentialDeployment` reads it, which is the same
// generation `candidatesFor` aims at. Three readings of "the target" is already one too
// many; a fourth, computed differently, is how a screen comes to report progress
// towards a generation no push is placing.
func (s *Server) activeGeneration(ctx context.Context, credID string) (int, error) {
	var gens, err = s.Store.ListGenerations(ctx, credID)
	if err != nil {
		return 0, err
	}
	var i int
	for i = range gens {
		if gens[i].State == "active" && gens[i].DeviceID == "" {
			return gens[i].Generation, nil
		}
	}
	return 0, nil
}

// credDeployTargets joins the credential's SCOPE with what has been OBSERVED on it.
//
// The asymmetry with [store.PlanDeployment] is deliberate and runs the other way.
// There, observation can only ADD to the caller's scope, because forgetting a device
// must not let a promotion through. Here, scope can only LIMIT what is pushed: a device
// observed under this credential but no longer resolving to it has been retagged or
// regrouped, and pushing this credential's secret to it would undo an operator's
// decision. It still shows up in the deployment view, still blocks promotion, and is
// exactly the case the per-device gesture exists for.
//
// Resolution is not re-implemented: `devicesScopedTo` puts every device through
// `store.CredentialsForDevice`, the single place the tag → group → site rule lives.
func (s *Server) credDeployTargets(ctx context.Context, siteID string,
	cred model.SiteCredential) ([]credDeployTarget, error) {
	var scope, err = s.devicesScopedTo(ctx, siteID, cred)
	if err != nil {
		return nil, err
	}
	var inScope = map[string]bool{}
	var id string
	for _, id = range scope {
		inScope[id] = true
	}
	var observed []store.DeviceGeneration
	observed, err = s.Store.InstalledGenerations(ctx, cred.ID)
	if err != nil {
		return nil, err
	}
	var seen = map[string]store.DeviceGeneration{}
	var g store.DeviceGeneration
	for _, g = range observed {
		seen[g.DeviceID] = g
	}
	var devices []model.Device
	devices, err = s.Store.ListDevices(ctx, siteID)
	if err != nil {
		return nil, err
	}
	var out = []credDeployTarget{}
	var d model.Device
	for _, d = range devices {
		if !inScope[d.ID] {
			continue
		}
		var obs = seen[d.ID]
		out = append(out, credDeployTarget{
			DeviceID:            d.ID,
			DeviceName:          d.Name,
			Status:              d.Status,
			MgmtIP:              d.MgmtIP,
			InstalledGeneration: obs.Generation,
			InstalledUsername:   obs.InstalledUsername,
		})
	}
	return out, nil
}

// runCredDeploy walks the queue, one device at a time.
func (s *Server) runCredDeploy(ctx context.Context, run *credDeployRun) {
	defer s.credDeploy.finish(run.CredentialID)
	if run.cancel != nil {
		defer run.cancel()
	}
	var why string
	var consecutive int
	var installed int
	var t credDeployTarget
	for _, t = range run.queued {
		if ctx.Err() != nil {
			why = "stopped by the operator: " + itoa(installed) +
				" device(s) took generation " + itoa(run.Target) +
				" before it stopped, and they keep it"
			break
		}
		// The operator's own gesture comes first, and this queue is the device's
		// serialization — the same stance as the PoE sampler and the config poll. A
		// second waiter beside it would be a second serialization mechanism, and the
		// device it is waiting on is one an operator is actively changing.
		if s.applyQueuedOrRunning(t.DeviceID) {
			run.record(credDeployResult{
				DeviceID: t.DeviceID, DeviceName: t.DeviceName,
				Outcome: credDeploySkipped, At: time.Now(),
				Detail: "a configuration apply is running or due on this device: its " +
					"session comes first. Run the deployment again to pick it up",
			})
			continue
		}
		var res = s.deployToDevice(ctx, run, t)
		run.record(res)
		switch res.Outcome {
		case credDeployInstalled, credDeployRehearsed:
			consecutive = 0
			installed++
		case credDeploySkipped:
			// Declined before anything was attempted on the wire — it says nothing
			// about the health of the run.
		default:
			consecutive++
		}
		if credDeployHalts(res.Outcome) {
			why = "HALTED on \"" + res.DeviceName + "\": a write was accepted and did " +
				"not read back. The same platform will do the same thing on the next " +
				"device, so the run stops here and a human reads the transcript"
			break
		}
		if credDeployTripped(consecutive) {
			why = "stopped after " + itoa(consecutive) + " devices refused in a row: " +
				"that is one cause, not three coincidences, and it is on the " +
				"controller's side of the wire. Fix it and run again"
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(credDeployPause):
		}
	}
	run.end(why)
	var st = run.snapshot()
	var sid = run.SiteID
	var sev = model.SevNotice
	if why != "" {
		sev = model.SevWarning
	}
	var msg = "Fleet deployment of generation " + itoa(run.Target) + " for \"" +
		run.Label + "\" finished: " + itoa(installed) + "/" + itoa(len(st.Queued)) +
		" device(s), " + itoa(len(st.Skipped)) + " skipped"
	if why != "" {
		msg += " — " + why
	}
	// DETACHED, because the commonest way for a run to end is the operator stopping it
	// — and that cancels `ctx`. Logging the outcome through a context the stop just
	// killed would delete the journal entry exactly in the case where somebody wants to
	// know how far the run got.
	_ = s.Store.InsertEvent(context.WithoutCancel(ctx), model.Event{
		SiteID: &sid, Type: "credential.deploy_finished", Severity: sev,
		Message: msg, Actor: run.Actor,
	})
	s.NotifySiteChanged(run.SiteID, ChangeDevice)
	slog.Info("credential deploy: run finished", "credential", run.Label,
		"generation", run.Target, "write", run.Write, "done", installed,
		"queued", len(st.Queued), "skipped", len(st.Skipped), "stopped", why)
}

// deployToDevice changes the secret of ONE device's existing administration account.
//
// Every refusal below happens BEFORE the write, and each one leaves the device carrying
// exactly what it carried when the run arrived.
//
// It does not go through `handleApplyDeviceCredential`, and the difference is the
// safety argument rather than a refactor that was not done. That handler is the
// operator's tool: it can create an account, delete the one it entered through, adapt
// the credential's scope when the platform's password policy refuses the site secret,
// and take a current password typed by a human. Every one of those is right with
// somebody watching, and none of them belongs in a sweep. THIS PATH HAS NONE OF THEM.
// It never touches [driver.AccountManager] — the type is not referenced here — so the
// rename sequence that once rewrote `mcadmin` to `admin` is not a decision this code
// makes badly; it is a decision this code cannot make.
func (s *Server) deployToDevice(parent context.Context, run *credDeployRun,
	t credDeployTarget) credDeployResult {
	var res = credDeployResult{
		DeviceID: t.DeviceID, DeviceName: t.DeviceName, At: time.Now(),
	}

	// THE OPERATOR'S STOP DOES NOT REACH INSIDE A DEVICE, and these two lines are where
	// that promise is kept. `WithoutCancel` detaches the device's work from the run's
	// context: once this function starts, it runs to its own timeout, and the loop
	// notices the cancellation between devices. The alternative — letting a stop abort a
	// session mid-gesture — is the one shape that can lose a secret: the device applies
	// the write, the read-back is cancelled, and the row is never updated, so the vault
	// and the switch disagree with nothing recording it. A stop that takes twenty
	// seconds longer is not comparable to that.
	//
	// `record` is detached and UNBOUNDED for the sharper half of the same reason: it
	// carries the database writes, which must survive even this device's own budget
	// running out. What they write is the only trace that a switch's password changed.
	var record = context.WithoutCancel(parent)
	var ctx, cancel = context.WithTimeout(record, credDeployDeviceBudget)
	defer cancel()

	var skip = func(reason string) credDeployResult {
		res.Outcome, res.Detail = credDeploySkipped, reason
		return res
	}
	var failed = func(reason string) credDeployResult {
		res.Outcome, res.Detail = credDeployFailed, reason
		// A device that could not be reached gets a READABLE cause on its row,
		// without any claim about what it carries (cf. NoteCredentialFailure). The
		// outstanding list then names the device AND the reason, which is what the
		// hand run had and what an automatic one must not lose.
		var noteErr = s.Store.NoteCredentialFailure(record, t.DeviceID,
			run.CredentialID, reason)
		if noteErr != nil {
			slog.Warn("credential deploy: failure not recorded",
				"device", t.DeviceName, "err", noteErr)
		}
		return res
	}

	// The device row is re-read: the queue is a snapshot, and a run lasts minutes.
	var d, err = s.Store.GetDevice(ctx, t.DeviceID)
	if err != nil {
		return failed("device no longer readable: " + err.Error())
	}
	// The eligibility rule is applied AGAIN, against the row as it is now. Cheap, and
	// it closes the window in which a device went off, was retagged, or took the
	// secret from an operator's own gesture while the run was working through the
	// queue ahead of it.
	var fresh = credDeployTarget{
		DeviceID: d.ID, DeviceName: d.Name, Status: d.Status, MgmtIP: d.MgmtIP,
		InstalledGeneration: t.InstalledGeneration, InstalledUsername: t.InstalledUsername,
	}
	var stillOK, why = credDeployEligible(fresh, run.Target)
	if !stillOK {
		return skip(why)
	}

	var conn, connErr = s.connectorFor(ctx, d)
	if connErr != nil {
		return failed(connErr.Error())
	}
	var caps = conn.Capabilities(d.Model)
	var groupName = s.groupNameOfCtx(ctx, d)
	var cred, _, credErr = s.credentialForDevice(ctx, d, groupName)
	if credErr != nil {
		return failed(credErr.Error())
	}
	if cred.ID != run.CredentialID {
		return skip("this device now resolves to credential \"" + credLabel(cred) +
			"\": it left this run's scope while the run was working")
	}

	// The candidate list is the SAME one the per-device gesture builds — same order,
	// same try budget, same last-resort factory slot (§15). No operator secret: a
	// fleet run has none, and inventing one would be guessing.
	var wanted = caps.Admin.EffectiveUsername(cred.Username)
	var cands, secret, gen, credID, buildErr = s.candidatesFor(ctx, d, wanted, groupName, "")
	if buildErr != nil {
		return failed(buildErr.Error())
	}
	if credID != run.CredentialID {
		return skip("credential resolution moved under the run")
	}
	if gen != run.Target {
		return skip("the active generation is now " + itoa(gen) + ", not " +
			itoa(run.Target) + ": the credential rotated while this run was working")
	}

	// THE PLATFORM'S PASSWORD POLICY, consulted before the first byte. The per-device
	// gesture answers a refusal here by generating an adapted technical-tag credential
	// (pwpolicy_creds.go) — which RETAGS the device and changes which credential
	// governs it. That is an operator's decision about scope, not something a sweep
	// does in passing to twenty switches, so this path only refuses and says what the
	// per-device gesture would do about it.
	var polErr = pwpolicy.For(caps.Admin).Accepts(secret)
	if polErr != nil {
		return skip("this platform refuses the secret before send (" + polErr.Error() +
			"): the per-device gesture can move this device to an adapted " +
			"technical-tag credential, which changes its scope and is not a decision " +
			"a fleet run makes")
	}

	var proxyID = s.proxyOf(ctx, d)
	var dialer, dialErr = s.dialerFor(proxyID)
	if dialErr != nil {
		return failed(dialErr.Error())
	}
	var tgt = driver.Target{
		Host: d.MgmtIP, Dialer: dialer, Model: d.Model, PortPrefix: portPrefix(d),
	}

	var adm driver.Admin
	adm, err = conn.Authenticate(ctx, tgt, cands)
	if adm != nil {
		defer adm.Close()
	}
	res.Steps = transcriptOf(adm)
	if err != nil {
		return failed("no session: " + err.Error())
	}

	// THE LAST GATE BEFORE A WRITE. See [credDeployAccount]: the run changes the
	// secret of the account it entered through, and only once that is the account on
	// record. Nothing has been written at this point, and a refusal here leaves the
	// device untouched.
	var account, nameErr = credDeployAccount(adm.Account(), t.InstalledUsername)
	if nameErr != nil {
		return skip(nameErr.Error())
	}
	res.Account = account

	if !run.Write {
		res.Outcome = credDeployRehearsed
		res.Detail = "session opened as \"" + account + "\"; generation " +
			itoa(run.Target) + " would be placed on that account. Nothing written"
		return res
	}

	// The CURRENT secret comes from the contract, never from the transcript: SwOS
	// turns it into the RC4 key of the digest it expects and answers HTTP 405 without
	// it (cf. the comment on the same line in handleApplyDeviceCredential, which
	// records how that was got wrong once).
	if err = adm.SetSecret(ctx, adm.Accepted().Secret, secret); err != nil {
		res.Steps = adm.Transcript()
		return failed("the secret was refused: " + err.Error())
	}
	res.Steps = adm.Transcript()

	// NOTHING IS RECORDED ON THE STRENGTH OF A RETURN CODE. A second session, with
	// this one candidate, is what proves the secret is in place — an accepted form
	// with no effect is a failure mode observed on Instant On, and a database
	// convinced of a success that never happened costs a site visit.
	var verify driver.Admin
	verify, err = conn.Authenticate(ctx, tgt, []driver.Candidate{
		{Username: account, Secret: secret, Why: "read-back of the deployed generation"},
	})
	if verify != nil {
		defer verify.Close()
	}
	res.Steps = append(append([]driver.Step{}, res.Steps...), transcriptOf(verify)...)
	if err != nil {
		res.Outcome = credDeployUnconfirmed
		res.Detail = "the device accepted the new secret and did not open under it: " +
			err.Error() + ". The previous generation stays valid — it is still " +
			"`retiring`, so it is still tried on every login"
		var noteErr = s.Store.NoteCredentialFailure(record, t.DeviceID,
			run.CredentialID, res.Detail)
		if noteErr != nil {
			slog.Warn("credential deploy: unconfirmed write not recorded",
				"device", d.Name, "err", noteErr)
		}
		s.recordDeployEvent(record, run, d, "credential.apply_unconfirmed",
			model.SevWarning, "Generation "+itoa(run.Target)+" placed on \""+d.Name+
				"\" but NOT confirmed: "+err.Error())
		return res
	}

	// OBSERVED, therefore recorded — in that order, always. The generation first
	// (which creates the row), then the account name the device actually answered to,
	// which is what an administrator will need to log in by hand.
	if err = s.Store.NoteInstalledGeneration(record, d.ID, credID, run.Target, ""); err != nil {
		res.Outcome = credDeployFailed
		res.Detail = "the device took generation " + itoa(run.Target) +
			" but meshcraft could not record it: " + err.Error() +
			". Re-read the deployment view before promoting anything"
		return res
	}
	if err = s.Store.NoteInstalledUsername(record, d.ID, credID, account); err != nil {
		slog.Warn("credential deploy: installed account not recorded",
			"device", d.Name, "err", err)
	}
	res.Outcome = credDeployInstalled
	res.Generation = run.Target
	s.recordDeployEvent(record, run, d, "credential.installed", model.SevNotice,
		"Credential \""+account+"\" (generation "+itoa(run.Target)+
			") placed and confirmed on \""+d.Name+"\" by the fleet deployment")
	return res
}

// recordDeployEvent journals a device event from the run.
//
// Not [Server.emitDeviceEvent]: that one reads the actor from the request's
// authenticated user, and a run has no request. The actor is the operator who STARTED
// the run, carried on the run — a fleet-wide secret push with "meshcraft" as its only
// actor would be an audit trail that names nobody.
func (s *Server) recordDeployEvent(ctx context.Context, run *credDeployRun,
	d model.Device, evType string, sev int, msg string) {
	var siteID, deviceID = d.SiteID, d.ID
	var err = s.Store.InsertEvent(ctx, model.Event{
		SiteID: &siteID, DeviceID: &deviceID, Type: evType, Severity: sev,
		Message: msg, Actor: run.Actor,
	})
	if err != nil {
		slog.Error("credential deploy: event not logged", "type", evType, "err", err)
	}
}

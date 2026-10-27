package store

import (
	"context"
	"strconv"
	"time"
)

// Fleet-wide deployment of a credential generation: the READ side and the GATE.
//
// # Why this file exists separately from generations.go
//
// `ListGenerations` answers "how many devices carry generation 4". That count is
// enough to forbid a purge (§6 of docs/en/03-Security/Service identity and rotation.md)
// and it is NOT enough to run, resume or judge a fleet deployment: resuming from a
// count means re-pushing to everything, and "everything" includes the devices whose
// push is the risky part. What a deployment needs is the IDENTITY — which devices
// are on 4, which are still on 3, which have never been reached.
//
// The per-device fact has existed since migration 0011 (`device_credential_state.
// installed_generation`); what was missing was a way to read it as a list. That is
// `InstalledGenerations`.
//
// # THE ORDERING, which is the whole design and is NOT "rotate then push"
//
// A rotation that moved the vault head and retired the old secret before every
// device carried the new one would strand whatever was offline at that moment, and
// the recovery is a console visit per switch. The order that survives interruption:
//
//	1. mint the new generation `pending` — nothing an existing session depends on changes;
//	2. push it device by device, RECORDING PER DEVICE which generation it now carries;
//	3. promote to `active` only once every device in scope has CONFIRMED;
//	4. only then mark the previous one `retiring` — never `retired`, and never on the
//	   strength of a push that was not read back.
//
// Step 3 is the one that cannot be got backwards, and it is the only step this file
// implements: [PlanDeployment] and [DeploymentPlan.SafeToPromote] decide whether the
// promotion is allowed, from OBSERVED state alone.
//
// # What is deliberately NOT here, and why
//
// No `AddPendingGeneration`, and no `PromoteGeneration` that writes. Minting a
// `pending` generation today would create a secret NOTHING pushes: `candidatesFor`
// (internal/api/devicecreds_handlers.go) selects its target as the first generation
// whose state is `active`, so a pending one is offered as a login candidate and never
// as a target. A pending generation could therefore never reach step 2, and step 3
// could never succeed — the feature would be inert, and inert credential machinery is
// worse than none, because the screen would show a rotation in progress that no push
// will ever complete.
//
// Wiring steps 1, 2 and 4 means changing which generation `candidatesFor` aims at,
// on the live apply path — the one hand-validated over eight devices on 2026-08-08.
// That belongs with the deployment RUNNER, in one change, with its own captures. The
// gate is built first and separately on purpose: it is pure, it is testable without a
// database or a device, and it is the piece the runner must not be free to reinvent.

// DeviceGeneration is what ONE device is OBSERVED to carry for one credential.
//
// Observed, never decreed: every field here is written by
// [Store.NoteInstalledGeneration] / [Store.NoteInstalledUsername] after a real
// session proved it, exactly like configuration drift (§13.4).
type DeviceGeneration struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	// Generation is 0 when the device has never been reached under this credential.
	// Zero is not a generation number — `credential_generations.generation` starts at
	// 1 — so it is unambiguous as "no observation".
	Generation int `json:"generation"`
	// InstalledUsername is the account name the secret was actually placed under. It
	// often differs from the desired one: many lines only accept `admin` (§13.4).
	InstalledUsername string     `json:"installed_username"`
	InstalledAt       *time.Time `json:"installed_at"`
	LastVerifiedAt    *time.Time `json:"last_verified_at"`
	LastError         string     `json:"last_error"`
}

// InstalledGenerations lists, per device, which generation of this credential the
// device is OBSERVED to carry — the identity that `ListGenerations` reduces to a
// count.
//
// SCOPE IS OBSERVATION, NOT RESOLUTION, and that is a deliberate limit. A device
// appears here once meshcraft has seen it under this credential; a device that
// merely RESOLVES to this credential and was never reached does not. Inverting the
// resolution rule here — tag, then group, then site default — would be a second copy
// of `CredentialsForDevice`, and the comment on that function says what the day of
// divergence costs: meshcraft trying one credential's generations while pushing
// another. The caller that knows the intended scope passes it to [PlanDeployment],
// which folds the two together and can only ever ADD devices to what was observed.
//
// Ordered lowest generation first, then by name: the devices that lag are the ones
// the operator is looking for, and they are at the top.
func (s *Store) InstalledGenerations(ctx context.Context, credentialID string) ([]DeviceGeneration, error) {
	var rows, err = s.pool.Query(ctx,
		`select st.device_id::text, d.name, coalesce(st.installed_generation, 0),
		        st.installed_username, st.installed_at, st.last_verified_at, st.last_error
		 from device_credential_state st
		 join devices d on d.id = st.device_id
		 where st.credential_id = $1::uuid
		 order by coalesce(st.installed_generation, 0), d.name`, credentialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []DeviceGeneration{}
	for rows.Next() {
		var g DeviceGeneration
		if err = rows.Scan(&g.DeviceID, &g.DeviceName, &g.Generation,
			&g.InstalledUsername, &g.InstalledAt, &g.LastVerifiedAt, &g.LastError); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// DeploymentPlan is the readable state of a fleet halfway through a rotation.
//
// "HALF THE FLEET ON EACH PASSWORD" IS A STATE THIS MAKES READABLE, NOT ONE IT
// PREVENTS. A fleet always has a switch that is off, a proxy that is down, or a
// device whose connector declares no AccountManager. A deployment that treats that as
// an error is a deployment nobody dares run twice; so partial failure is the normal
// case, every bucket below is expected to be non-empty at some point, and the only
// thing forbidden is PROMOTING while [Behind] or [Unreached] still holds anyone.
type DeploymentPlan struct {
	// Target is the generation being deployed.
	Target int `json:"target"`
	// Carrying: confirmed on the target generation.
	Carrying []DeviceGeneration `json:"carrying"`
	// Behind: still on an older generation. They keep working — the old generation
	// stays `retiring`, hence still valid — and they are what blocks promotion.
	Behind []DeviceGeneration `json:"behind"`
	// Unreached: in scope, never observed under this credential. Never confuse with
	// Carrying: "nothing confirmed" is not "everything confirmed".
	Unreached []DeviceGeneration `json:"unreached"`
	// Ahead: observed on a generation NEWER than the target. Not an error to fix here
	// and not a reason to block — such a device is not stranded by anything this
	// promotion does — but it means a run happened out of order, and silence about it
	// would leave the operator reading a total that does not add up.
	Ahead []DeviceGeneration `json:"ahead"`
}

// PlanDeployment classifies observed state against the generation being deployed.
//
// `scope` is the set of device ids the run intends to cover, resolved by the caller
// (see the scope note on [Store.InstalledGenerations]). It can only ADD: EVERY
// OBSERVED HOLDER IS COUNTED WHETHER OR NOT THE CALLER LISTED IT. That asymmetry is
// the guard, and it is the reason this function takes both lists instead of trusting
// one. A caller that computed its scope from a stale tag, or narrowed it to "the
// three devices I just pushed to", must not thereby be able to promote a generation
// that the other five devices meshcraft KNOWS about do not carry. Forgetting a device
// is exactly how a fleet gets stranded, so forgetting one cannot be made to look like
// success.
//
// Pure: no database, no device, no clock. The gate is the part of the mechanism that
// must be provable by reading it.
func PlanDeployment(target int, scope []string, observed []DeviceGeneration) DeploymentPlan {
	var p = DeploymentPlan{
		Target:    target,
		Carrying:  []DeviceGeneration{},
		Behind:    []DeviceGeneration{},
		Unreached: []DeviceGeneration{},
		Ahead:     []DeviceGeneration{},
	}
	var seen = map[string]bool{}
	var g DeviceGeneration
	for _, g = range observed {
		if g.DeviceID == "" || seen[g.DeviceID] {
			continue
		}
		seen[g.DeviceID] = true
		switch {
		case g.Generation <= 0:
			p.Unreached = append(p.Unreached, g)
		case g.Generation < target:
			p.Behind = append(p.Behind, g)
		case g.Generation > target:
			p.Ahead = append(p.Ahead, g)
		default:
			p.Carrying = append(p.Carrying, g)
		}
	}
	// Devices the caller means to cover but that were never observed. They are the
	// most dangerous entry in the list precisely because there is nothing to show for
	// them — no name, no error, no date — and a run that skipped them silently would
	// leave the vault asserting a secret they do not hold.
	var id string
	for _, id = range scope {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		p.Unreached = append(p.Unreached, DeviceGeneration{DeviceID: id})
	}
	return p
}

// Outstanding counts the devices that still stand between this generation and
// promotion.
func (p DeploymentPlan) Outstanding() int { return len(p.Behind) + len(p.Unreached) }

// SafeToPromote answers step 3 of the ordering: may this generation become `active`,
// moving the vault head and pushing the previous one to `retiring`?
//
// It says yes only when every device in scope has CONFIRMED the target generation,
// and it returns the reason for a refusal in operator language — a refusal nobody can
// read is a refusal somebody overrides.
//
// Note the last clause: an empty fleet does not promote. A plan with nothing in any
// bucket is not "everyone is up to date", it is "we have not looked", and the two
// must not produce the same answer. That case is not hypothetical: it is what a
// caller with a mistyped credential id, or one that resolved its scope before the
// devices were adopted, hands to this function.
func (p DeploymentPlan) SafeToPromote() (bool, string) {
	if p.Target <= 0 {
		return false, "no target generation: there is nothing to promote"
	}
	if len(p.Behind) > 0 || len(p.Unreached) > 0 {
		var why = "generation " + strconv.Itoa(p.Target) + " is not installed everywhere: "
		if len(p.Behind) > 0 {
			why += strconv.Itoa(len(p.Behind)) + " device(s) still on an older generation"
			if len(p.Unreached) > 0 {
				why += ", "
			}
		}
		if len(p.Unreached) > 0 {
			why += strconv.Itoa(len(p.Unreached)) + " device(s) never reached"
		}
		return false, why + ". They keep working on the previous generation, which stays " +
			"valid — promote once they have taken it, or take them out of scope deliberately"
	}
	if len(p.Carrying) == 0 {
		return false, "no device has confirmed generation " + strconv.Itoa(p.Target) +
			": nothing observed is not the same as everything installed"
	}
	return true, ""
}

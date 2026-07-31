package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"meshcraft/internal/model"
)

// Secret generations (cf. migration 0011 and
// docs/03-Security/Identite de service et rotation sections 3 to 6).

const genCols = `g.id::text, g.credential_id::text, coalesce(g.device_id::text, ''),
	g.generation, g.state, g.created_at, g.activated_at, g.retired_at`

func scanGeneration(row pgx.Row) (model.CredentialGeneration, error) {
	var g model.CredentialGeneration
	var err = row.Scan(&g.ID, &g.CredentialID, &g.DeviceID, &g.Generation, &g.State,
		&g.CreatedAt, &g.ActivatedAt, &g.RetiredAt)
	return g, notFound(err)
}

// ListGenerations returns a credential's history, most recent first, with the
// count of devices still on each generation. That count is what blocks purge:
// "3 devices still on generation 4" reads directly.
func (s *Store) ListGenerations(ctx context.Context, credentialID string) ([]model.CredentialGeneration, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+genCols+`,
		   (select count(*) from device_credential_state d
		    where d.credential_id = g.credential_id
		      and d.installed_generation = g.generation) as installed_on
		 from credential_generations g
		 where g.credential_id = $1::uuid
		 order by g.generation desc, g.device_id nulls first`, credentialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.CredentialGeneration{}
	for rows.Next() {
		var g model.CredentialGeneration
		if err = rows.Scan(&g.ID, &g.CredentialID, &g.DeviceID, &g.Generation, &g.State,
			&g.CreatedAt, &g.ActivatedAt, &g.RetiredAt, &g.InstalledOn); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// AddGeneration creates the next generation of a credential.
//
// The previous active generation moves to `retiring` — NOT `retired`: devices still
// carry it, and it remains the only way to reach them until they are updated. That
// is the whole point of this mechanism.
//
// `secret2Enc` is nil except for `snmp_v3`. Both phrases of a v3 identity rotate
// TOGETHER and are archived together: a generation carrying only one would leave
// unanswered, at the next rotation, the only question that matters — which encryption
// phrase goes with this one.
func (s *Store) AddGeneration(ctx context.Context, credentialID, deviceID string,
	secretEnc, secret2Enc []byte, createdBy string) (model.CredentialGeneration, error) {
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return model.CredentialGeneration{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var g model.CredentialGeneration
	g, err = addGenerationTx(ctx, tx, credentialID, deviceID, secretEnc, secret2Enc, createdBy)
	if err != nil {
		return model.CredentialGeneration{}, err
	}
	// The vault follows the current generation.
	//
	// Without this, `RevealSiteCredential` would keep returning the OLD pair after a
	// rotation: revelation would show a stale secret, and SNMP push — which reads the
	// vault, not generations — would replay the previous one. The gap went unnoticed
	// while only the admin password existed, because its push path reads generations
	// directly.
	if _, err = tx.Exec(ctx,
		`update site_credentials
		    set secret_enc = $2,
		        secret2_enc = coalesce($3, secret2_enc),
		        rotated_at = now()
		  where id = $1::uuid`,
		credentialID, secretEnc, secret2Enc); err != nil {
		return model.CredentialGeneration{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return model.CredentialGeneration{}, err
	}
	return g, nil
}

// addGenerationTx is the shared body: changing the secret from the edit screen and
// clicking "new generation" are the SAME act. Two distinct paths would eventually
// diverge, and the day they diverge history lies about what is actually on devices.
func addGenerationTx(ctx context.Context, tx pgx.Tx, credentialID, deviceID string,
	secretEnc, secret2Enc []byte, createdBy string) (model.CredentialGeneration, error) {
	var next int
	var err = tx.QueryRow(ctx,
		`select coalesce(max(generation), 0) + 1 from credential_generations
		 where credential_id = $1::uuid
		   and coalesce(device_id::text, '') = $2`,
		credentialID, deviceID).Scan(&next)
	if err != nil {
		return model.CredentialGeneration{}, err
	}

	// Active becomes retiring: still valid, but no longer pushed.
	if _, err = tx.Exec(ctx,
		`update credential_generations set state = 'retiring'
		 where credential_id = $1::uuid
		   and coalesce(device_id::text, '') = $2
		   and state in ('active', 'pending')`,
		credentialID, deviceID); err != nil {
		return model.CredentialGeneration{}, err
	}

	var devArg any
	if deviceID != "" {
		devArg = deviceID
	}
	var createdArg any
	if createdBy != "" {
		createdArg = createdBy
	}
	var g model.CredentialGeneration
	// The new one is `active` immediately: it is the one we will push. Effective
	// installation is observed device by device.
	if err = tx.QueryRow(ctx,
		`insert into credential_generations (credential_id, device_id, generation,
		     secret_enc, secret2_enc, state, activated_at, created_by)
		 values ($1::uuid, $2::uuid, $3, $4, $6, 'active', now(), $5::uuid)
		 returning id::text, credential_id::text, coalesce(device_id::text, ''),
		           generation, state, created_at, activated_at, retired_at`,
		credentialID, devArg, next, secretEnc, createdArg, secret2Enc).Scan(
		&g.ID, &g.CredentialID, &g.DeviceID, &g.Generation, &g.State,
		&g.CreatedAt, &g.ActivatedAt, &g.RetiredAt); err != nil {
		return model.CredentialGeneration{}, err
	}
	return g, nil
}

// UsableSecret is a secret to try, with the reason for its position in the order.
// The reason is shown in session transcripts: knowing WHICH generation worked is
// the only way to fix observed state.
type UsableSecret struct {
	CredentialID string
	Generation   int
	Username     string
	SecretEnc    []byte
	State        string
	// Why explains the position: "installed on this device", "stepping back, may still
	// be in place", etc.
	Why string
}

// scanUsableSecrets reads generations returned by the order query and translates
// each rank into a readable reason. The reason is shown in transcripts: knowing
// WHICH generation worked is the only way to fix observed state.
func scanUsableSecrets(rows pgx.Rows) ([]UsableSecret, error) {
	var out = []UsableSecret{}
	var err error
	for rows.Next() {
		var u UsableSecret
		var isInstalled bool
		if err = rows.Scan(&u.CredentialID, &u.Username, &u.Generation, &u.SecretEnc,
			&u.State, &isInstalled); err != nil {
			return nil, err
		}
		switch {
		case isInstalled:
			u.Why = "generation observed on this device"
		case u.State == "retiring":
			u.Why = "generation stepped back: device may not be updated yet"
		case u.State == "active":
			u.Why = "current generation: may have been pushed without confirmation"
		default:
			u.Why = "generation " + itoa(u.Generation)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// NoteInstalledGeneration records what a device ACTUALLY carries.
//
// Called after a successful session, including — and especially — when the generation
// that worked is not the expected one: the database learns from reality, exactly like
// for configuration drift.
func (s *Store) NoteInstalledGeneration(ctx context.Context, deviceID, credentialID string,
	generation int, errMsg string) error {
	var _, err = s.pool.Exec(ctx,
		`insert into device_credential_state (device_id, credential_id,
		     installed_generation, installed_at, last_verified_at, last_error)
		 values ($1::uuid, $2::uuid, $3, now(), now(), $4)
		 on conflict (device_id, credential_id) do update set
		   installed_generation = $3,
		   installed_at = case when device_credential_state.installed_generation
		                       is distinct from $3 then now()
		                  else device_credential_state.installed_at end,
		   last_verified_at = now(),
		   last_error = $4`,
		deviceID, credentialID, generation, errMsg)
	return err
}

// RetirableGenerations lists generations that COULD be purged, and those still
// blocked with the reason. A generation is never purged while a known device still
// carries it: a switch powered off for three months reboots with its old password.
func (s *Store) RetirableGenerations(ctx context.Context, credentialID string) (map[int]string, error) {
	var rows, err = s.pool.Query(ctx,
		`select g.generation,
		        (select count(*) from device_credential_state d
		         where d.credential_id = g.credential_id
		           and d.installed_generation = g.generation) as holders
		 from credential_generations g
		 where g.credential_id = $1::uuid and g.state = 'retiring'`, credentialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = map[int]string{}
	for rows.Next() {
		var gen, holders int
		if err = rows.Scan(&gen, &holders); err != nil {
			return nil, err
		}
		if holders == 0 {
			out[gen] = ""
			continue
		}
		out[gen] = itoa(holders) + " device(s) still carry this generation"
	}
	return out, rows.Err()
}

// RevealGeneration returns the encrypted secrets of a specific generation. Like
// credential revelation, audit trail is the caller's job.
//
// The PAIR is returned, never one without the other: on a v3 device left behind,
// replaying the old auth phrase with the new encryption phrase does not produce a
// readable error, it produces silence — the agent drops the packet it cannot decrypt.
func (s *Store) RevealGeneration(ctx context.Context, credentialID string,
	generation int) ([]byte, []byte, string, error) {
	var enc, enc2 []byte
	var siteID string
	var err = s.pool.QueryRow(ctx,
		`select g.secret_enc, g.secret2_enc, c.site_id::text
		 from credential_generations g join site_credentials c on c.id = g.credential_id
		 where g.credential_id = $1::uuid and g.generation = $2 and g.device_id is null`,
		credentialID, generation).Scan(&enc, &enc2, &siteID)
	return enc, enc2, siteID, notFound(err)
}

// CredentialFor picks the credential that applies to a device: its GROUP's if one
// exists, the site's otherwise. This is the rule from section 12 of
// docs/03-Security/Identite de service et rotation, and it exists because password
// policies differ from one model to another.
func (s *Store) CredentialFor(ctx context.Context, siteID, groupName, kind string) (model.SiteCredential, error) {
	return scanSiteCred(s.pool.QueryRow(ctx,
		`select `+siteCredCols+` from site_credentials
		 where site_id = $1::uuid and kind = $3
		   and group_name in ($2, '')
		 order by (group_name = $2) desc
		 limit 1`, siteID, groupName, kind))
}

// InstalledUsername re-reads the account name actually set on a device, empty if
// none yet.
//
// This enables reconnecting to a RENAMED device: after an account rename, desired
// and installed names match, but in between — or if push failed halfway — they
// differ. Trying the observed name before the desired one avoids concluding bad
// password where the account does not exist.
func (s *Store) InstalledUsername(ctx context.Context, deviceID, credentialID string) (string, error) {
	var name *string
	var err = s.pool.QueryRow(ctx,
		`select installed_username from device_credential_state
		 where device_id = $1::uuid and credential_id = $2::uuid`,
		deviceID, credentialID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil || name == nil {
		return "", err
	}
	return *name, nil
}

// NoteInstalledUsername records the account name ACTUALLY set.
//
// It often differs from the desired name: many lines only accept `admin`. Without
// this trace, an administrator would try to connect with an account that does not
// exist on the device.
func (s *Store) NoteInstalledUsername(ctx context.Context, deviceID, credentialID, username string) error {
	var _, err = s.pool.Exec(ctx,
		`update device_credential_state set installed_username = $3
		 where device_id = $1::uuid and credential_id = $2::uuid`,
		deviceID, credentialID, username)
	return err
}

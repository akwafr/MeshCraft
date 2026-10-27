package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"meshcraft/internal/model"
	"meshcraft/internal/tagging"
)

// Device tags and tagging rules.
// See docs/en/01-Architecture/Tags and credential scope.md.

// DeviceTags returns a device's tags in stable order.
func (s *Store) DeviceTags(ctx context.Context, deviceID string) ([]tagging.Tag, error) {
	var rows, err = s.pool.Query(ctx,
		`select tag, source from device_tags where device_id = $1::uuid order by tag`,
		deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out = []tagging.Tag{}
	for rows.Next() {
		var t tagging.Tag
		if err = rows.Scan(&t.Tag, &t.Source); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TagsBySite returns tags for ALL devices in a site, indexed by device. One query
// instead of one per device: the fleet view needs this for two hundred rows.
func (s *Store) TagsBySite(ctx context.Context, siteID string) (map[string][]tagging.Tag, error) {
	var rows, err = s.pool.Query(ctx,
		`select t.device_id::text, t.tag, t.source
		 from device_tags t join devices d on d.id = t.device_id
		 where d.site_id = $1::uuid
		 order by t.device_id, t.tag`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out = map[string][]tagging.Tag{}
	for rows.Next() {
		var id string
		var t tagging.Tag
		if err = rows.Scan(&id, &t.Tag, &t.Source); err != nil {
			return nil, err
		}
		out[id] = append(out[id], t)
	}
	return out, rows.Err()
}

// SiteTagNames returns tags known on a site — for autocomplete.
//
// Three sources, deliberately united:
//   - tags already on devices (what operators have typed);
//   - tags assigned by rules (what automation would set);
//   - enrollment tags of switch profiles (what applying a profile expects).
//
// Omitting the third made autocomplete miss the tag an operator had just
// enrolled on a switch profile: they would invent a synonym, apply it, and
// nothing would fire — the profile looks for the exact enrolled string.
func (s *Store) SiteTagNames(ctx context.Context, siteID string) ([]string, error) {
	var rows, err = s.pool.Query(ctx,
		`select distinct tag from (
		   select t.tag from device_tags t join devices d on d.id = t.device_id
		    where d.site_id = $1::uuid
		   union
		   select r.tag from tag_rules r where r.site_id = $1::uuid
		   union
		   select p.tag from switch_profiles p
		    where p.site_id = $1::uuid and p.tag is not null and p.tag <> ''
		 ) x order by tag`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out = []string{}
	for rows.Next() {
		var t string
		if err = rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetDeviceTag sets a tag. Setting an existing tag again updates its source — that
// is how a derived tag becomes manual when the operator confirms it, and then
// stops being removable by a rule.
//
// ONE TAG IS REFUSED HERE, AND IT IS REFUSED IN THE STORE ON PURPOSE.
// [FallbackAdminTag] is a SCOPE KEY on a `site_credentials` row, never a device
// filing. Setting it on a device would make [Store.CredentialsForDevice] rank the
// fallback credential at specificity 3 against the site row's 1 — the emergency
// door would become the credential every session opens with and every fleet
// deployment writes to, while the account that actually governs the fleet went
// untracked. That is one call away from anywhere, so the refusal sits at the only
// place all of those calls pass through rather than in each of them.
func (s *Store) SetDeviceTag(ctx context.Context, deviceID, tag, source string) error {
	if IsFallbackAdminTag(tag) {
		return ErrFallbackTagIsNotADeviceTag
	}
	var _, err = s.pool.Exec(ctx,
		`insert into device_tags (device_id, tag, source) values ($1::uuid, $2, $3)
		 on conflict (device_id, tag) do update set source = excluded.source`,
		deviceID, tag, source)
	return err
}

// RemoveDeviceTag removes a tag.
func (s *Store) RemoveDeviceTag(ctx context.Context, deviceID, tag string) error {
	var _, err = s.pool.Exec(ctx,
		`delete from device_tags where device_id = $1::uuid and tag = $2`, deviceID, tag)
	return err
}

// ApplyTagPlan applies what a re-evaluation decided in ONE transaction. All or
// nothing: a half re-tagged fleet is worse than an untouched fleet, because nobody
// would know where it stopped.
func (s *Store) ApplyTagPlan(ctx context.Context, deviceID string, plan tagging.Plan) error {
	if plan.Empty() {
		return nil
	}
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var t tagging.Tag
	for _, t = range plan.Remove {
		if _, err = tx.Exec(ctx,
			`delete from device_tags where device_id = $1::uuid and tag = $2 and source = $3`,
			deviceID, t.Tag, t.Source); err != nil {
			return err
		}
	}
	for _, t = range plan.Add {
		if _, err = tx.Exec(ctx,
			`insert into device_tags (device_id, tag, source) values ($1::uuid, $2, $3)
			 on conflict (device_id, tag) do update set source = excluded.source`,
			deviceID, t.Tag, t.Source); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// --- Rules ---

// TagRule is a rule as stored and returned by the API.
type TagRule struct {
	ID       string        `json:"id"`
	SiteID   string        `json:"site_id"`
	Tag      string        `json:"tag"`
	Priority int           `json:"priority"`
	Enabled  bool          `json:"enabled"`
	Match    tagging.Match `json:"match"`
	Note     string        `json:"note"`
	// Problems: what prevents this rule from working, in plain language. A rule that
	// never matches and says nothing looks configured.
	Problems []string `json:"problems,omitempty"`
}

// Rule converts to the decision engine form.
func (r TagRule) Rule() tagging.Rule {
	return tagging.Rule{ID: r.ID, Tag: r.Tag, Match: r.Match, Enabled: r.Enabled}
}

func scanTagRule(row pgx.Row) (TagRule, error) {
	var r TagRule
	var raw []byte
	var err = row.Scan(&r.ID, &r.SiteID, &r.Tag, &r.Priority, &r.Enabled, &raw, &r.Note)
	if err != nil {
		return r, notFound(err)
	}
	if err = json.Unmarshal(raw, &r.Match); err != nil {
		return r, err
	}
	r.Problems = r.Match.Problems()
	return r, nil
}

const tagRuleCols = `id::text, site_id::text, tag, priority, enabled, match, note`

// ListTagRules returns a site's rules in application order.
func (s *Store) ListTagRules(ctx context.Context, siteID string) ([]TagRule, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+tagRuleCols+` from tag_rules where site_id = $1::uuid
		 order by priority desc, tag`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out = []TagRule{}
	for rows.Next() {
		var r TagRule
		if r, err = scanTagRule(rows); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertTagRule creates or updates a rule.
func (s *Store) UpsertTagRule(ctx context.Context, r TagRule, updatedBy string) (TagRule, error) {
	var raw, err = json.Marshal(r.Match)
	if err != nil {
		return TagRule{}, err
	}
	var by *string
	if updatedBy != "" {
		by = &updatedBy
	}
	if r.ID != "" {
		return scanTagRule(s.pool.QueryRow(ctx,
			`update tag_rules set tag = $2, priority = $3, enabled = $4, match = $5,
			        note = $6, updated_by = $7
			 where id = $1::uuid returning `+tagRuleCols,
			r.ID, r.Tag, r.Priority, r.Enabled, raw, r.Note, by))
	}
	return scanTagRule(s.pool.QueryRow(ctx,
		`insert into tag_rules (site_id, tag, priority, enabled, match, note, updated_by)
		 values ($1::uuid, $2, $3, $4, $5, $6, $7)
		 returning `+tagRuleCols,
		r.SiteID, r.Tag, r.Priority, r.Enabled, raw, r.Note, by))
}

// GetTagRule reads a rule by id.
func (s *Store) GetTagRule(ctx context.Context, id string) (TagRule, error) {
	return scanTagRule(s.pool.QueryRow(ctx,
		`select `+tagRuleCols+` from tag_rules where id = $1::uuid`, id))
}

// DeleteTagRule deletes a rule AND the tags it applied: a deleted rule that left
// its tags behind would make the fleet inexplicable — tags with no rule and
// nothing left to justify them.
func (s *Store) DeleteTagRule(ctx context.Context, id string) error {
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err = tx.Exec(ctx,
		`delete from device_tags where source = $1`, "rule:"+id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `delete from tag_rules where id = $1::uuid`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// --- Credential scope ---

// CredentialsForDevice returns credentials that TARGET this device, from most
// specific to most general (cf. architecture note section 3):
//
//  1. credential targeting a tag the device carries;
//  2. credential targeting its group;
//  3. site default credential.
//
// Order is TOTAL: at equal specificity, highest priority, then tag name. That last
// criterion is not taste — it exists so no case stays unresolved. Two runs that
// picked different credentials would push two different passwords on the same device.
//
// The ENTIRE list is returned, not just the winner, so the caller can flag
// ambiguity instead of hiding it.
func (s *Store) CredentialsForDevice(ctx context.Context, deviceID, siteID, groupName,
	kind string) ([]model.SiteCredential, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+siteCredCols+`,
		        case when tag <> '' then 3 when group_name <> '' then 2 else 1 end as specificity
		 from site_credentials
		 where site_id = $1::uuid and kind = $4
		   and (
		         (tag <> '' and tag in (select tag from device_tags where device_id = $2::uuid))
		      or (tag = '' and group_name <> '' and group_name = $3)
		      or (tag = '' and group_name = '')
		   )
		 order by specificity desc, priority desc, tag, id`,
		siteID, deviceID, groupName, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out = []model.SiteCredential{}
	for rows.Next() {
		var c model.SiteCredential
		var specificity int
		if err = rows.Scan(&c.ID, &c.SiteID, &c.GroupName, &c.Kind, &c.Username,
			&c.HasSecret, &c.HasSecret2, &c.Note, &c.RotatedAt, &c.CreatedAt, &c.PerDevice,
			&c.Tag, &c.Priority, &c.TransportTLS, &c.CAPEM, &specificity); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PreviousInstalls returns what a device is known to carry under OTHER credentials
// than the one that applies today, with the observed account name.
//
// This happens as soon as a device's scope changes: giving it a tag switches it
// from one credential to another, and the new one was never pushed to it. Without
// this fallback, re-tagging an already provisioned device would lose access —
// meshcraft would try a secret it never pushed and ignore the one it pushed itself.
// On a line with no serial console, recovery means a factory reset.
//
// This is the "never cut off your own hand" rule applied to scope: changing your
// mind about where a device BELONGS must never cost its access.
func (s *Store) PreviousInstalls(ctx context.Context, deviceID,
	exceptCredentialID string) ([]UsableSecret, error) {
	var rows, err = s.pool.Query(ctx,
		`select st.credential_id::text,
		        coalesce(nullif(st.installed_username, ''), c.username),
		        g.generation, g.secret_enc, g.state, true
		 from device_credential_state st
		 join site_credentials c on c.id = st.credential_id
		 join credential_generations g
		   on g.credential_id = st.credential_id
		  and g.generation = st.installed_generation
		 where st.device_id = $1::uuid
		   and st.credential_id <> $2::uuid
		   and st.installed_generation is not null
		   and g.state <> 'compromised'
		 order by st.installed_at desc nulls last`,
		deviceID, exceptCredentialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out = []UsableSecret{}
	for rows.Next() {
		var u UsableSecret
		var installed bool
		if err = rows.Scan(&u.CredentialID, &u.Username, &u.Generation, &u.SecretEnc,
			&u.State, &installed); err != nil {
			return nil, err
		}
		u.Why = "observed under another credential on this site"
		out = append(out, u)
	}
	return out, rows.Err()
}

// SecretsForCredential returns secrets to try on a device, IN ORDER, for an
// ALREADY resolved credential.
//
// This is the core mechanism: between two rotations the fleet is never in a single
// state, and connecting means trying what the device PROBABLY carries before what
// we want it to carry (docs/en/03-Security/Service identity and rotation.md section 13.3):
//
//  1. the generation it is KNOWN to carry (observed state);
//  2. retiring generations, newest to oldest — the device may not have been cleaned up yet;
//  3. active generations not yet observed — a push interrupted after write but before confirmation;
//  4. nothing else: the caller completes with factory credentials.
//
// Credential resolution used to live in DUPLICATE: once in CredentialFor, once in
// this query's CTE. Two copies of the same rule eventually diverge, and the day
// they diverge meshcraft tries one credential's generations while pushing another.
// One place chooses now: CredentialsForDevice.
func (s *Store) SecretsForCredential(ctx context.Context, deviceID, credentialID string) ([]UsableSecret, error) {
	var rows, err = s.pool.Query(ctx,
		`with cred as (
		   select c.id, c.username, c.per_device from site_credentials c
		   where c.id = $2::uuid
		 ),
		 installed as (
		   select installed_generation from device_credential_state
		   where device_id = $1::uuid and credential_id = $2::uuid
		 )
		 select cred.id::text, cred.username, g.generation, g.secret_enc, g.state,
		        (g.generation is not distinct from (select installed_generation from installed)) as is_installed
		 from credential_generations g, cred
		 where g.credential_id = cred.id
		   and g.state <> 'retired' and g.state <> 'compromised'
		   and (g.device_id is null or g.device_id = $1::uuid)
		 order by is_installed desc,
		          case g.state when 'retiring' then 0 when 'active' then 1 else 2 end,
		          g.generation desc`,
		deviceID, credentialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsableSecrets(rows)
}

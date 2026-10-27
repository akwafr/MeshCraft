package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"meshcraft/internal/model"
)

// deviceCols also serves as the `returning` clause (cf. CreateDevice, UpdateDevice and
// the Set* methods), and that dictates proxy name shape: a `returning` clause cannot
// carry a join, but it accepts a correlated subquery. The same fragment therefore
// works for `select` and `returning`, and the collector name arrives without
// duplicating this list in two variants — two column lists would eventually drift,
// and scanning is shared.
//
// Side benefit, decisive here: device inserts written elsewhere (siteconfig_store.go,
// discovery_store.go) enumerate their columns and return only `id::text`. They therefore
// ignore additions and did not need to be touched.
const deviceCols = `id::text, site_id::text, group_id::text, name, model, vendor, location,
	mgmt_ip, status, status_note, status_at,
	snmp_status, snmp_status_note, snmp_status_at,
	poe_budget_w, pos_x, pos_y, port_groups, ports, extra,
	observed, observed_at, desired_changed_at, applied_at, last_apply,
	coalesce(address_domain_id::text, ''),
	coalesce(proxy_id::text, ''),
	coalesce((select p.name from proxies p where p.id = devices.proxy_id), ''),
	coalesce(trap_dest_addr, ''), coalesce(trap_dest_proxy_id::text, ''),
	trap_dest_at, trap_seen_at,
	mgmt_addr_mode, mgmt_static_ip, mgmt_static_gw, mgmt_static_dns,
	mgmt_addr_by::text, mgmt_addr_at,
	mgmt_addr_observed, mgmt_addr_observed_at`

func scanDevice(row pgx.Row) (model.Device, error) {
	var d model.Device
	var pg, ports, extra, observed, lastApply, mgmtObs []byte
	var err = row.Scan(&d.ID, &d.SiteID, &d.GroupID, &d.Name, &d.Model, &d.Vendor, &d.Location,
		&d.MgmtIP, &d.Status, &d.StatusNote, &d.StatusAt,
		&d.SnmpStatus, &d.SnmpStatusNote, &d.SnmpStatusAt,
		&d.PoEBudgetW, &d.PosX, &d.PosY, &pg, &ports, &extra,
		&observed, &d.ObservedAt, &d.DesiredChangedAt, &d.AppliedAt, &lastApply,
		&d.AddressDomainID, &d.ProxyID, &d.ProxyName,
		&d.TrapDestAddr, &d.TrapDestProxyID, &d.TrapDestAt, &d.TrapSeenAt,
		&d.MgmtAddr.Mode, &d.MgmtAddr.StaticIP, &d.MgmtAddr.StaticGateway,
		&d.MgmtAddr.StaticDNS, &d.MgmtAddr.By, &d.MgmtAddr.At,
		&mgmtObs, &d.MgmtAddrObservedAt)
	d.PortGroups, d.Ports, d.Extra, d.Observed = pg, ports, extra, observed
	d.LastApply = lastApply
	d.MgmtAddrObserved = mgmtObs
	return d, notFound(err)
}

func (s *Store) ListDevices(ctx context.Context, siteID string) ([]model.Device, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+deviceCols+` from devices where site_id = $1::uuid order by name`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.Device{}
	for rows.Next() {
		var d, scanErr = scanDevice(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDevice(ctx context.Context, id string) (model.Device, error) {
	return scanDevice(s.pool.QueryRow(ctx,
		`select `+deviceCols+` from devices where id = $1::uuid`, id))
}

// CreateDevice adds a device. Its status is NOT written here: the column defaults to
// "unknown", and that is the only true state of a device nobody has pinged yet.
// `d.Status` is therefore ignored — deliberately, cf. UpdateDevice.
func (s *Store) CreateDevice(ctx context.Context, d model.Device) (model.Device, error) {
	return scanDevice(s.pool.QueryRow(ctx,
		`insert into devices (site_id, group_id, name, model, vendor, location, mgmt_ip,
		    poe_budget_w, pos_x, pos_y, port_groups, ports, extra)
		 values ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		 returning `+deviceCols,
		d.SiteID, d.GroupID, d.Name, d.Model, d.Vendor, d.Location, d.MgmtIP,
		d.PoEBudgetW, d.PosX, d.PosY,
		jsonOr(d.PortGroups, `[]`), jsonOr(d.Ports, `{}`), jsonOr(d.Extra, `{}`)))
}

// UpdateDevice replaces editable fields (ports handled separately).
//
// `status` AND `status_note` are NOT among them; removing them is a correction.
//
// They used to be there, fed by the request body: any form save therefore rewrote
// reachability with the value the client read when opening the screen. The site plan
// explicitly sends `status` in its PUT (web/src/pages/Plan.tsx) — moving an icon was
// enough to republish a stale observation and flip back to "unknown" a device
// monitoring had just declared online. SetDeviceStatus's comment promised "an
// observation, never a deduction"; a form could contradict it.
//
// Status is not EDITED, it is OBSERVED: the only write path is now SetDeviceStatus,
// called by monitoring. The corresponding fields also left the API input
// (api.deviceInput), so refusal is visible where clients are written rather than
// discovered here.
func (s *Store) UpdateDevice(ctx context.Context, d model.Device) (model.Device, error) {
	return scanDevice(s.pool.QueryRow(ctx,
		`update devices set group_id = $2::uuid, name = $3, model = $4, vendor = $5,
		    location = $6, mgmt_ip = $7,
		    poe_budget_w = $8, pos_x = $9, pos_y = $10, port_groups = $11
		 where id = $1::uuid returning `+deviceCols,
		d.ID, d.GroupID, d.Name, d.Model, d.Vendor, d.Location, d.MgmtIP,
		d.PoEBudgetW, d.PosX, d.PosY, jsonOr(d.PortGroups, `[]`)))
}

// SetDeviceFaceplate records the physical port groups read from the device.
//
// The faceplate is a FACT of the hardware, not intent — same nature as observed
// state. It is normally set once at adoption from discovery enrichment; this
// method exists so a re-read can fill it on a device adopted before its connector
// could read the faceplate (or entered by hand), without touching name/location/
// intent. Returns the fresh device so the caller can redraw.
func (s *Store) SetDeviceFaceplate(ctx context.Context, deviceID string, groups json.RawMessage) (model.Device, error) {
	return scanDevice(s.pool.QueryRow(ctx,
		`update devices set port_groups = $2::jsonb where id = $1::uuid returning `+deviceCols,
		deviceID, jsonOr(groups, `[]`)))
}

// ReplaceDevicePorts OVERWRITES the entire `ports` column.
//
// Distinct from SetDevicePort(s), which PATCH specific keys via jsonb_set and
// never remove any — the right semantics for an operator editing a
// selection. This method exists for the one case that needs the opposite: the
// one-time standard-port-name catch-up (cf. api.remapDevicePortsIfStale),
// where a stale key must actually disappear, renamed to the form the
// connector uses today, or the device would carry both the old entry and its
// renamed twin.
func (s *Store) ReplaceDevicePorts(ctx context.Context, deviceID string, ports json.RawMessage) (model.Device, error) {
	return scanDevice(s.pool.QueryRow(ctx,
		`update devices set ports = $2::jsonb where id = $1::uuid returning `+deviceCols,
		deviceID, jsonOr(ports, `{}`)))
}

// SetDevicePort replaces one port's configuration in the jsonb `ports`.
func (s *Store) SetDevicePort(ctx context.Context, deviceID, portName string, port json.RawMessage) (model.Device, error) {
	return scanDevice(s.pool.QueryRow(ctx,
		`update devices set ports = jsonb_set(ports, array[$2]::text[], $3::jsonb, true)
		 where id = $1::uuid returning `+deviceCols,
		deviceID, portName, []byte(port)))
}

// SetDevicePorts applies several ports IN ONE TRANSACTION.
//
// Selecting a port range is one user gesture: applying it halfway would be worse than
// doing nothing, because you would then have to guess which ports took and which were
// left behind.
func (s *Store) SetDevicePorts(ctx context.Context, deviceID string,
	ports map[string]json.RawMessage) (model.Device, error) {
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return model.Device{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var name string
	var raw json.RawMessage
	for name, raw = range ports {
		var tag, execErr = tx.Exec(ctx,
			`update devices set ports = jsonb_set(ports, array[$2]::text[], $3::jsonb, true)
			 where id = $1::uuid`, deviceID, name, []byte(raw))
		if execErr != nil {
			return model.Device{}, execErr
		}
		if tag.RowsAffected() == 0 {
			return model.Device{}, ErrNotFound
		}
	}
	var d model.Device
	d, err = scanDevice(tx.QueryRow(ctx,
		`select `+deviceCols+` from devices where id = $1::uuid`, deviceID))
	if err != nil {
		return model.Device{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return model.Device{}, err
	}
	return d, nil
}

// SetDeviceAddressDomain declares which network this device's address lives in.
// Empty string = no declared domain.
//
// Separate entry point from UpdateDevice, deliberately. UpdateDevice rewrites all
// fields passed to it; slipping domain in would erase a declaration on every device
// rename by a screen that ignores it. And it is a distinct gesture anyway: saying
// which network an address lives in is not the same as fixing a label.
func (s *Store) SetDeviceAddressDomain(ctx context.Context, deviceID, domainID string) (model.Device, error) {
	return scanDevice(s.pool.QueryRow(ctx,
		`update devices set address_domain_id = $2::uuid where id = $1::uuid
		 returning `+deviceCols, deviceID, nullableUUID(domainID)))
}

// SetDeviceAddressing records a device's management addressing INTENT (migration 0027):
// DHCP, static with its three fields, or inherited.
//
// Separate entry point from UpdateDevice, and here the reason is harder than elsewhere.
// UpdateDevice rewrites all fields passed to it; slipping addressing in would erase an
// addressing decision on every device rename by a screen that ignores it — that is,
// by every existing screen. On labels, such regression is annoyance; here, intent will
// decide what a push writes into a switch admin interface.
//
// AUTHOR AND DATE ARE WRITTEN IN THE SAME `update`, never in a second call: addressing
// intent without an author reads six months later like a factory value, and nobody
// knows if it is still wanted.
//
// `mgmt_ip` IS NOT TOUCHED, and that is the invariant not to break. It carries the
// OBSERVED address — the one meshcraft actually reaches the device at, written by
// discovery and relocation. Copying the desired static address into it would make the
// device look reachable at an address nothing has configured, and monitoring would ping
// an intent.
func (s *Store) SetDeviceAddressing(ctx context.Context, deviceID string,
	a model.DeviceAddressing, updatedBy string) (model.Device, error) {
	if a.StaticDNS == nil {
		a.StaticDNS = []string{}
	}
	return scanDevice(s.pool.QueryRow(ctx,
		`update devices set mgmt_addr_mode = $2, mgmt_static_ip = $3,
		    mgmt_static_gw = $4, mgmt_static_dns = $5,
		    mgmt_addr_by = $6::uuid, mgmt_addr_at = now()
		 where id = $1::uuid returning `+deviceCols,
		deviceID, a.Mode, a.StaticIP, a.StaticGateway, a.StaticDNS,
		nullableUUID(updatedBy)))
}

// SaveMgmtAddrObserved records what the DEVICE declares as its management addressing,
// as a connector just read it.
//
// Same rule as SaveObserved, for the same reason: a READ never touches intent.
// Otherwise a device in static mode would absorb on its own the "DHCP" intent an
// operator just set, drift would disappear, and the screen would show "conformant" on
// a device nobody reconfigured.
func (s *Store) SaveMgmtAddrObserved(ctx context.Context, deviceID string, observed []byte) error {
	var tag, err = s.pool.Exec(ctx,
		`update devices set mgmt_addr_observed = $2, mgmt_addr_observed_at = now()
		 where id = $1::uuid`, deviceID, observed)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ClearMgmtAddrObserved drops the last addressing snapshot.
//
// Used when the operator changes intent or `mgmt_ip` by hand: keeping the previous
// observation would raise a false drift against an address that is no longer current.
func (s *Store) ClearMgmtAddrObserved(ctx context.Context, deviceID string) error {
	var tag, err = s.pool.Exec(ctx,
		`update devices set mgmt_addr_observed = null, mgmt_addr_observed_at = null
		 where id = $1::uuid`, deviceID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SaveSTPObserved records spanning-tree observation under devices.extra.stp_observed.
//
// Kept in Extra rather than a dedicated column: STP is an optional platform fact,
// refreshed on read-state like management addressing, and must not enter VLAN drift.
func (s *Store) SaveSTPObserved(ctx context.Context, deviceID string, observed []byte) error {
	if len(observed) == 0 {
		observed = []byte("{}")
	}
	var tag, err = s.pool.Exec(ctx,
		`update devices set extra = jsonb_set(
		    coalesce(extra, '{}'::jsonb), '{stp_observed}', $2::jsonb, true)
		 where id = $1::uuid`, deviceID, observed)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SaveSTPSuperRoot records the operator's SUPER ROOT intent for one device under
// devices.extra.stp_super_root.
//
// # This one is an INTENT, and every other tenant of this shelf is an OBSERVATION
//
// `stp_observed`, `poe_observed`, `optics_observed` and `dhcp_snooping_observed`
// are all "what the device said last time somebody asked". This is the opposite:
// nobody read it off a switch, an operator ticked a box. Putting it here anyway
// is a deliberate departure and worth naming, because a reviewer will notice.
//
// It goes here because Extra is EXPLICITLY OUTSIDE THE CONFORMITY DIFF (cf.
// deviceaddr.go and the conformity walk): a device carrying this flag must not
// show drift on account of it. A dedicated column would put it inside the model
// the diff compares, and "this switch is the region root" is not a divergence
// from anything — it is a decision.
//
// # Why it is stored at all rather than derived
//
// The only alternative signal is an OBSERVED bridge priority of 0, and reading
// the checkbox back off that would be a deduction dressed as a record: a switch
// somebody set to priority 0 by hand years ago would render as though meshcraft
// had been asked for it, and un-ticking the box would then "restore" a value
// meshcraft never chose. The checkbox has to render the INTENT.
func (s *Store) SaveSTPSuperRoot(ctx context.Context, deviceID string, on bool) error {
	var raw = "false"
	if on {
		raw = "true"
	}
	var tag, err = s.pool.Exec(ctx,
		`update devices set extra = jsonb_set(
		    coalesce(extra, '{}'::jsonb), '{stp_super_root}', $2::jsonb, true)
		 where id = $1::uuid`, deviceID, raw)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SaveLAGIntent records the DESIRED aggregate list under devices.extra.lags.
//
// An INTENT on the observation shelf, like stp_super_root and under its
// argument: Extra is outside the conformity diff, and an aggregate intent
// must not surface as port drift while no LAG conformity rule exists. The
// OBSERVED aggregates ride devices.observed.lags (driver.State carries them),
// so the pair reads exactly like ports do — desired beside observed —
// without either entering the other's comparisons.
func (s *Store) SaveLAGIntent(ctx context.Context, deviceID string, lags []byte) error {
	if len(lags) == 0 {
		lags = []byte("[]")
	}
	var tag, err = s.pool.Exec(ctx,
		`update devices set extra = jsonb_set(
		    coalesce(extra, '{}'::jsonb), '{lags}', $2::jsonb, true)
		 where id = $1::uuid`, deviceID, lags)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SavePoEObserved records the last PoE observation under devices.extra.poe_observed.
//
// Same shelf as SaveSTPObserved, and for the same reason: an optional platform
// fact, refreshed on read-state, that must never enter VLAN drift. What is
// stored is the whole driver.PoEState — per-port scope (Capable), configured
// class, and the measured snapshot that came with the read. The measured part
// is NOT a history and must not be read as one: it is "what the device said
// the last time someone asked", refreshed at read-state cadence, and the
// per-port history that would answer "since when" belongs to the metrics
// pipeline (internal/tsdb), not here. Persisting the snapshot anyway is what
// lets the device sheet say "this jack has no PSE hardware" and "configured
// passive 24V" without forcing a live round-trip to a switch that may be
// unreachable at that moment.
func (s *Store) SavePoEObserved(ctx context.Context, deviceID string, observed []byte) error {
	if len(observed) == 0 {
		observed = []byte("{}")
	}
	var tag, err = s.pool.Exec(ctx,
		`update devices set extra = jsonb_set(
		    coalesce(extra, '{}'::jsonb), '{poe_observed}', $2::jsonb, true)
		 where id = $1::uuid`, deviceID, observed)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SaveDHCPSnoopObserved records the last DHCP-snooping observation under
// devices.extra.dhcp_snooping_observed.
//
// Same shelf as SaveSTPObserved and SavePoEObserved, and for the same reason:
// an optional platform fact, refreshed when someone asks, that must never
// enter VLAN drift. What is stored is the whole driver.DHCPSnoopState — the
// GLOBAL enable, the VLANs the feature actually inspects, and the per-port
// trust flags — and storing the three together is the entire point. A screen
// that kept only the port flags would show "trusted" on a device whose
// global snooping is off, which is the precise failure the driver contract
// (internal/driver/dhcpsnoop.go, § "the per-port control is TRUST") exists to
// prevent: an exact configuration and zero protection.
//
// A port ABSENT from the stored Ports list was NOT OBSERVED, and that is not
// the same as untrusted. Nothing here defaults it — the reader on the other
// end must keep the distinction, because "untrusted" is the protected state
// and "not read" is an unknown.
//
// NO MIGRATION: `devices.extra` is jsonb and this is one more key on the same
// shelf, exactly like the three above.
func (s *Store) SaveDHCPSnoopObserved(ctx context.Context, deviceID string, observed []byte) error {
	if len(observed) == 0 {
		observed = []byte("{}")
	}
	var tag, err = s.pool.Exec(ctx,
		`update devices set extra = jsonb_set(
		    coalesce(extra, '{}'::jsonb), '{dhcp_snooping_observed}', $2::jsonb, true)
		 where id = $1::uuid`, deviceID, observed)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SaveOpticsObserved records the last transceiver-diagnostics observation
// under devices.extra.optics_observed.
//
// Same shelf as SaveSTPObserved and SavePoEObserved, and the same doctrine:
// an optional platform fact, refreshed on read-state, that must never enter
// VLAN drift — a receive power that moves a tenth of a dBm between two reads
// is a fibre being a fibre, not a configuration change. And like the PoE
// snapshot, this is "what the device said the last time someone asked", not
// a history: per-port power over time belongs to the metrics pipeline
// (internal/tsdb) the day a collector can take a CLI-sourced sample.
// Persisting the snapshot is what lets the device sheet say "FS SFP-10G-BX
// in XG1, RX -1.85 dBm at last read" while the switch is unreachable.
func (s *Store) SaveOpticsObserved(ctx context.Context, deviceID string, observed []byte) error {
	if len(observed) == 0 {
		observed = []byte("{}")
	}
	var tag, err = s.pool.Exec(ctx,
		`update devices set extra = jsonb_set(
		    coalesce(extra, '{}'::jsonb), '{optics_observed}', $2::jsonb, true)
		 where id = $1::uuid`, deviceID, observed)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ErrProxyUnknown: the designated collector proxy does not exist.
//
// The foreign key catches it, and that is intentional: a control `select` before
// write would leave a window between check and update, and would lie precisely in the
// case it pretends to cover (a proxy deleted while the attachment screen was open).
// But a raw 23503 violation reaches the operator as "insert or update on table
// devices violates foreign key constraint", which translates to neither an HTTP
// response nor a readable sentence. Hence the translation here, once.
var ErrProxyUnknown = errors.New("this proxy does not exist")

// SetDeviceProxy declares a device's REFERENCE COLLECTOR proxy: the one that polls
// it via SNMP and whose address will be written into the device as its trap destination
// (cf. migration 0022 and DevicesWatchedBy).
//
// Empty string = DETACH, not an error. Detaching is as normal as attaching — return to
// coverage-based mode, the only fallback — and refusing it would force the layer above
// to invent a second entry point to say "no one".
//
// Separate entry point from UpdateDevice, for the exact reason as SetDeviceAddressDomain:
// UpdateDevice rewrites all fields passed to it, and slipping attachment in would erase
// it on every device rename by a screen that ignores it. A collector lost without anyone
// deciding it means SNMP collection stops silently.
//
// # Recorded trap destination is FORGOTTEN in the same step
//
// Changing collector changes the address to write into the device: what is already there
// becomes wrong, and acquired confirmation (`trap_seen_at`) is worthless — it referred to
// the return path toward the OLD proxy (cf. migration 0023). All three columns are
// therefore reset to null in the SAME `update`, not in a second call the next caller
// would forget: what an attachment invalidates, it always invalidates, and a proof kept
// after a path change is exactly the kind of lie this batch exists to remove.
//
// What is NOT done here: re-push. The device keeps emitting toward the old address until
// someone rewrites the line — and the forget step above is what makes that state READABLE
// rather than masked by a recorded destination that no longer matches anything.
//
// The forget predicate uses `trap_dest_proxy_id` not the old `proxy_id`, and the
// difference matters: the recorded destination stays valid if and only if it comes from the
// proxy that is collector after this `update`. Written this way, replaying the same
// attachment — an screen resubmitting the current value — does not destroy a still-accurate
// proof, and detaching always destroys it (no collector, therefore no unique address to
// write: the destination no longer designates anything reproducible).
func (s *Store) SetDeviceProxy(ctx context.Context, deviceID, proxyID string) error {
	// nullableUUID (declared in discovery_store.go) is required: an empty string sent as-is
	// to a `::uuid` parameter is REJECTED by PostgreSQL, not interpreted as absence. Its
	// documentation says "no change" because its original caller wraps it in `coalesce`;
	// here there is none, so NULL reaches the column and DETACHES. That is the intended
	// gesture, and direct assignment is the only way to express it.
	var tag, err = s.pool.Exec(ctx,
		`update devices set proxy_id = $2::uuid,
		    trap_dest_addr = case when trap_dest_proxy_id is distinct from $2::uuid
		                          then null else trap_dest_addr end,
		    trap_dest_at = case when trap_dest_proxy_id is distinct from $2::uuid
		                        then null else trap_dest_at end,
		    trap_seen_at = case when trap_dest_proxy_id is distinct from $2::uuid
		                        then null else trap_seen_at end,
		    trap_dest_proxy_id = case when trap_dest_proxy_id is distinct from $2::uuid
		                              then null else trap_dest_proxy_id end
		 where id = $1::uuid`,
		deviceID, nullableUUID(proxyID))
	// No constraint name check: this `update` writes two columns referencing `proxies`,
	// but the second (`trap_dest_proxy_id`) only receives `null` or its previous value —
	// neither can violate its foreign key. The only possible 23503 is therefore from the
	// requested attachment. Checking `devices_proxy_id_fkey` would freeze a PostgreSQL-
	// generated name in Go, without learning anything more.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return ErrProxyUnknown
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetTrapDestination records what was just WRITTEN into the device: the address
// declared as its trap recipient, and the proxy it came from (cf. migration 0023).
//
// Called AFTER a successful push, never before: what this row claims is that the device
// carries the configuration — the connector verified by re-read — not that the return
// path works. Proof arrives later by another path (ConfirmTrapPath).
//
// Confirmation is KEPT when pushing exactly the same destination, and CLEARED as soon as
// address or proxy change. Same reasoning as SetDeviceProxy and worth repeating here,
// because it is the other half of the same invariant: proof refers to a path. Replaying
// the same line — what every replayed provisioning does — must not lose an established
// fact nor generate a second "path established" announcement; changing address must reset
// everything to prove, otherwise meshcraft would show as confirmed a destination nothing
// ever validated.
func (s *Store) SetTrapDestination(ctx context.Context, deviceID, addr, proxyID string) error {
	var tag, err = s.pool.Exec(ctx,
		`update devices set trap_dest_addr = $2, trap_dest_proxy_id = $3::uuid,
		    trap_dest_at = now(),
		    trap_seen_at = case when trap_dest_addr = $2
		                         and trap_dest_proxy_id = $3::uuid
		                        then trap_seen_at else null end
		 where id = $1::uuid`, deviceID, addr, nullableUUID(proxyID))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return ErrProxyUnknown
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ConfirmTrapPath marks the return path as ESTABLISHED, and says whether it is the
// first time.
//
// The second return is what keeps the fleet journal from repeating acquired information.
// The proxy emits `tunnel.EventTrapSeen` only once per device and proxy, but it REARMS
// its counter as soon as the device leaves then re-enters its watch list — that is, on
// every tunnel reconnect and every fleet change. Relying on emitter thrift would therefore
// produce one "return path established" line per proxy restart, across the whole fleet.
//
// The bound is taken by the DATABASE not by a prior read followed by a Go test:
// `trap_seen_at is null` in the `where` makes "first time" a property of the `update`
// itself. Two traps arriving at once from two proxies therefore cannot journal twice, where
// a `select` then `update` would leave the window wide open.
//
// The condition on `trap_dest_proxy_id` is the other half of the meaning: what is
// confirmed is THE written destination. A trap accepted by a proxy whose address is not the
// one we pushed does not prove our claim — it proves someone else configured this device,
// or the recorded destination is stale. Both deserve notice, not recording as confirmation.
//
// # NO timestamp parameter, and that is the core of this signature
//
// The date is PostgreSQL's, like SetTrapDestination's `trap_dest_at`. It already was for
// the claim and was not for the proof: the controller wrote here the date from the proxy
// event — therefore from a machine deployed at a customer site, with no sync guarantee.
// Result seen on first real-hardware trial: `trap_seen_at` was eleven seconds BEFORE the
// `trap_dest_at` of the push it confirmed. Proof before what it proves, not by causality
// violation but by CLOCK MIXING — and two adjacent columns in the same row invite the
// comparison that makes no sense.
//
// The controller is the only clock that can order its own claims. A date from a proxy says
// what the PROXY believes: diagnostic information (the gap itself says much about fleet
// state, cf. api.trapClockNote), not a value comparable to what the controller wrote.
// The parameter therefore left the signature rather than being documented: as long as it
// does not exist, nobody can fill it with a third-party date.
func (s *Store) ConfirmTrapPath(ctx context.Context,
	deviceID, proxyID string) (bool, error) {
	var tag, err = s.pool.Exec(ctx,
		`update devices set trap_seen_at = now()
		 where id = $1::uuid and trap_dest_proxy_id = $2::uuid and trap_seen_at is null`,
		deviceID, nullableUUID(proxyID))
	if err != nil {
		return false, err
	}
	// Zero rows is NOT an error, and especially not ErrNotFound: the ordinary case of the
	// second trap, from an already confirmed device. The caller distinguishes the three
	// situations (already confirmed, unknown destination, another proxy's destination) on
	// the device it re-read anyway to name site and device in its journal.
	return tag.RowsAffected() == 1, nil
}

// MoveDevice attaches a device to another site. Its links are removed (a link connects
// two devices on the same site) and its group is detached (groups belong to the origin
// site). Port configuration is kept as-is: it references VLAN numbers the destination site
// must declare on its side.
func (s *Store) MoveDevice(ctx context.Context, deviceID, targetSiteID string) error {
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err = tx.Exec(ctx,
		`delete from device_links where a_device = $1::uuid or b_device = $1::uuid`,
		deviceID); err != nil {
		return err
	}
	var tag, execErr = tx.Exec(ctx,
		`update devices set site_id = $2::uuid, group_id = null where id = $1::uuid`,
		deviceID, targetSiteID)
	if execErr != nil {
		return execErr
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteDevice(ctx context.Context, id string) error {
	var tag, err = s.pool.Exec(ctx, `delete from devices where id = $1::uuid`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// --- Links (plan topology) ---

const linkCols = `id::text, site_id::text, a_device::text, a_port, b_device::text, b_port, speed, status, note`

func scanLink(row pgx.Row) (model.DeviceLink, error) {
	var l model.DeviceLink
	var err = row.Scan(&l.ID, &l.SiteID, &l.ADevice, &l.APort, &l.BDevice, &l.BPort,
		&l.Speed, &l.Status, &l.Note)
	return l, notFound(err)
}

func (s *Store) ListLinks(ctx context.Context, siteID string) ([]model.DeviceLink, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+linkCols+` from device_links where site_id = $1::uuid order by id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.DeviceLink{}
	for rows.Next() {
		var l, scanErr = scanLink(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) GetLink(ctx context.Context, id string) (model.DeviceLink, error) {
	return scanLink(s.pool.QueryRow(ctx,
		`select `+linkCols+` from device_links where id = $1::uuid`, id))
}

func (s *Store) CreateLink(ctx context.Context, l model.DeviceLink) (model.DeviceLink, error) {
	return scanLink(s.pool.QueryRow(ctx,
		`insert into device_links (site_id, a_device, a_port, b_device, b_port, speed, status, note)
		 values ($1::uuid, $2::uuid, $3, $4::uuid, $5, $6, $7, $8) returning `+linkCols,
		l.SiteID, l.ADevice, l.APort, l.BDevice, l.BPort, l.Speed, l.Status, l.Note))
}

func (s *Store) DeleteLink(ctx context.Context, id string) error {
	var tag, err = s.pool.Exec(ctx, `delete from device_links where id = $1::uuid`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// --- Site plan ---

func (s *Store) GetSitePlan(ctx context.Context, siteID string) (json.RawMessage, error) {
	var plan []byte
	var err = s.pool.QueryRow(ctx,
		`select plan from sites where id = $1::uuid`, siteID).Scan(&plan)
	return plan, notFound(err)
}

func (s *Store) UpdateSitePlan(ctx context.Context, siteID string, plan json.RawMessage) error {
	var _, err = s.pool.Exec(ctx,
		`update sites set plan = $2 where id = $1::uuid`, siteID, jsonOr(plan, `{}`))
	return err
}

// jsonOr returns a default jsonb value if raw is empty.
func jsonOr(raw json.RawMessage, def string) []byte {
	if len(raw) == 0 {
		return []byte(def)
	}
	return raw
}

// SetDeviceMgmtIP updates a device's OBSERVED management address. Used after relocation:
// the device is the same, only its address changed (cf. internal/relocate).
//
// It FORGETS trap destination and proof in the same `update`, for the same reason as
// SetDeviceProxy — and this gap was found re-reading documentation, not code.
//
// Changing management address breaks the trap channel at BOTH ends, and recorded proof
// survived both:
//
//   - the proxy recognizes a trap by its SOURCE IP, checked against its watch list
//     (cf. classifyTrap in cmd/proxy/trap.go). The device now emits from its new address:
//     its traps are dropped as "unwatched source" until a new WatchConfig is pushed;
//   - the address WRITTEN in the device was the one the proxy uses to reach it, therefore
//     the result of a routing table queried from the old network. Moving the device can
//     change that route, and therefore make false an address nothing rewrites.
//
// Keeping `trap_seen_at` would have shown "return path established, verified" on a device
// no trap reaches anymore. That is precisely the lie migration 0023 separates into two
// columns to make impossible: proof must stop being valid as soon as one of its
// presuppositions changes.
//
// `trap_dest_addr` is cleared too, not kept "since it is physically on the device": here
// the proxy did not change but the device position did, so the address itself is suspect.
// Re-pushing is an explicit gesture (trap-apply), not a deduction.
func (s *Store) SetDeviceMgmtIP(ctx context.Context, deviceID, ip string) error {
	var tag, err = s.pool.Exec(ctx,
		`update devices set mgmt_ip = $2,
		    trap_dest_addr = null, trap_dest_proxy_id = null,
		    trap_dest_at = null, trap_seen_at = null
		 where id = $1::uuid`, deviceID, ip)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// WatchedDevice: the minimum needed to watch a device.
// (declared in model)

// DevicesWatchedBy returns devices CANDIDATE for monitoring by a proxy: those with an
// address, on sites this proxy covers. An instance proxy covers all sites.
//
// Coverage decides eligibility, as elsewhere (cf. docs/en/01-Architecture/Proxy.md). It is not
// enough to make a target list: an instance proxy covering two sites both using
// 192.168.1.0/24 would receive two devices at the SAME address here, and the proxy pings
// by address. Final sorting belongs to netscope.SelectWatchTargets, which uses the address
// domain returned here.
//
// # The two regimes, and why they coexist in one query
//
// Since migration 0022, a device can designate its REFERENCE COLLECTOR (`devices.proxy_id`).
// The query does not filter on it, it PROJECTS it in `Collector`:
//
//	attached     -> Collector is true only for ITS proxy, and for no other, even if another covers it;
//	not attached -> Collector is true everywhere, exactly pre-0022 behavior.
//
// Filtering ROWS would have been this batch's trap: the same list feeds ICMP targets and,
// after netscope sorting, SNMP targets (cf. api.watchConfigFor). Restricting rows to the
// collector would also restrict ping, and lose reachability failover the day that proxy
// falls. Two pings cost nothing; two SNMP collections report every port transition twice
// and do not translate into one trap recipient address. The caller builds ICMP targets
// from the full list, SNMP targets from `Collector` rows — without a second query.
//
// Attachment does NOT grant coverage: a device attached to a proxy that does not cover its
// site appears in NONE of these lists, including its own. That is deliberate (coverage is a
// confidentiality boundary, see migration 0022) and means the layer proposing attachment
// must offer only covering proxies.
func (s *Store) DevicesWatchedBy(ctx context.Context, proxyID string) ([]model.WatchedDevice, error) {
	var rows, err = s.pool.Query(ctx,
		// mgmt_ip is TEXT in this schema, not inet: no host() here.
		//
		// `rp` is the ATTACHMENT proxy, not to be confused with `p`, the list recipient.
		// LEFT join: an unattached device must stay in the list — the case for the entire
		// pre-0022 fleet, and an inner join would silently remove it from monitoring.
		`select d.id::text, d.site_id::text, d.name, d.mgmt_ip,
		        coalesce(d.address_domain_id::text, ''),
		        coalesce(d.proxy_id::text, ''), coalesce(rp.name, ''),
		        (d.proxy_id is null or d.proxy_id = p.id)
		 from devices d
		 join proxies p on p.id = $1::uuid
		 left join proxies rp on rp.id = d.proxy_id
		 where coalesce(d.mgmt_ip, '') <> ''
		   and (p.global
		        or exists (select 1 from proxy_sites ps
		                   where ps.proxy_id = p.id and ps.site_id = d.site_id))
		 order by d.name`, proxyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.WatchedDevice{}
	for rows.Next() {
		var d model.WatchedDevice
		if err = rows.Scan(&d.ID, &d.SiteID, &d.Name, &d.MgmtIP, &d.AddressDomainID,
			&d.ProxyID, &d.ProxyName, &d.Collector); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetDeviceStatus records a device's OBSERVED state, and says whether it CHANGED.
// Ping monitoring feeds it: status is an observation, never a deduction.
//
// THE BOOLEAN IS WHAT PREVENTS FALSE OUTAGES. Monitoring re-announces every target's
// state on every tunnel reconnect (cf. tunnel.Event.Resync), otherwise a lost transition
// would never replay. But a journal treating every re-announcement as a change would
// describe outages that did not happen — and the write must still happen. Separating the
// two requires knowing whether the value moved, and knowing WITHOUT a prior read: between a
// `select` and an `update`, two concurrent events would both conclude "it changed" and
// journal the same transition twice.
//
// Hence the CTE: it captures the old row in the same atomic order as the write, and
// `returning` compares against that old state. Zero rows means device not found, never
// "nothing changed" — the two cases are distinct, where a `where status <> $2` would have
// conflated them.
//
// `status_at` MOVES ONLY IF THE OBSERVATION CHANGES, and that is what makes it readable.
// Advancing it on every pass — therefore on every reconnect re-announcement — would reset
// the date without anything happening: "unreachable since 2pm" would become "unreachable
// since 3s" at the first tunnel recovery, and displayed duration would measure WAN stability
// instead of device stability. The `case` is therefore in the write, not the caller: the
// same transaction decides whether it changed and dates the change, and the two cannot diverge.
func (s *Store) SetDeviceStatus(ctx context.Context, deviceID, status,
	note string) (bool, error) {
	var changed bool
	var err = s.pool.QueryRow(ctx,
		`with avant as (
		     select id, status, status_note, status_at from devices where id = $1::uuid
		 )
		 update devices d set status = $2, status_note = $3,
		     status_at = case
		         when avant.status <> $2 or avant.status_note <> $3 then now()
		         else avant.status_at
		     end
		 from avant
		 where d.id = avant.id
		 returning (avant.status <> $2 or avant.status_note <> $3)`,
		deviceID, status, note).Scan(&changed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	return changed, nil
}

// SetDeviceSNMPStatus records SNMP COLLECTION reachability, and says whether it CHANGED.
//
// Same atomic CTE as SetDeviceStatus, for the same reason: reconnect re-announcements
// must write without journaling false outages, and snmp_status_at must move only when
// the observation actually changes.
func (s *Store) SetDeviceSNMPStatus(ctx context.Context, deviceID, status,
	note string) (bool, error) {
	var changed bool
	var err = s.pool.QueryRow(ctx,
		`with avant as (
		     select id, snmp_status, snmp_status_note, snmp_status_at
		       from devices where id = $1::uuid
		 )
		 update devices d set snmp_status = $2, snmp_status_note = $3,
		     snmp_status_at = case
		         when avant.snmp_status <> $2 or avant.snmp_status_note <> $3 then now()
		         else avant.snmp_status_at
		     end
		 from avant
		 where d.id = avant.id
		 returning (avant.snmp_status <> $2 or avant.snmp_status_note <> $3)`,
		deviceID, status, note).Scan(&changed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	return changed, nil
}

// --- Observed state and drift attribution ---
// See docs/en/01-Architecture/Reconciliation.md sections 2 and 6.

// SaveObserved records what the device ACTUALLY carries.
//
// Writes ONLY observed state: desired configuration is never touched by a read. Re-reading
// a device must not be able to modify intent — otherwise drift would absorb itself and
// nobody would ever see it.
func (s *Store) SaveObserved(ctx context.Context, deviceID string, observed []byte) error {
	var _, err = s.pool.Exec(ctx,
		`update devices set observed = $2, observed_at = now() where id = $1::uuid`,
		deviceID, observed)
	return err
}

// TouchDesired notes that a change was made IN meshcraft. That date, compared to the last
// push date, distinguishes "meshcraft is ahead" from "the device drifted".
func (s *Store) TouchDesired(ctx context.Context, deviceID string) error {
	var _, err = s.pool.Exec(ctx,
		`update devices set desired_changed_at = now() where id = $1::uuid`, deviceID)
	return err
}

// AbsorbObserved copies observed state into intent: "absorb" from section 6.
//
// Both dates are set together, intent being by construction no longer ahead of the device.
// That is what makes a LATER divergence correctly attributed to the device, not to an
// unpushed meshcraft change.
//
// `ports` is provided by the caller rather than copied verbatim: observed state does not
// carry port labels, and losing them when absorbing drift would punish the operator for
// documenting their fleet.
func (s *Store) AbsorbObserved(ctx context.Context, deviceID string, ports []byte) error {
	var _, err = s.pool.Exec(ctx,
		`update devices set ports = $2, desired_changed_at = now(), applied_at = now()
		 where id = $1::uuid`, deviceID, ports)
	return err
}

// MarkApplied timestamps a SUCCESSFUL verified push.
//
// Call only after re-reading the device: this date is what switches subsequent drift from
// "meshcraft ahead" to "device drifted". Setting it on a return code alone would attribute
// to the operator a drift that is only a failed apply.
func (s *Store) MarkApplied(ctx context.Context, deviceID string) error {
	var _, err = s.pool.Exec(ctx,
		`update devices set applied_at = now() where id = $1::uuid`, deviceID)
	return err
}

// SaveApplyOutcome keeps trace of the last push, successful or not.
//
// Including what it SPARED: the only way to explain, on the screen where the question
// arises, why a port did not move.
func (s *Store) SaveApplyOutcome(ctx context.Context, deviceID string, outcome []byte) error {
	var _, err = s.pool.Exec(ctx,
		`update devices set last_apply = $2 where id = $1::uuid`, deviceID, outcome)
	return err
}

// ClearApplyOutcome forgets the last push.
//
// Called when intent is ABSORBED from the device: the failed push referred to a
// configuration the operator just abandoned, and keeping it would indefinitely show a
// failure that no longer matters — with a "replay" button that would re-push intent that
// no longer exists.
func (s *Store) ClearApplyOutcome(ctx context.Context, deviceID string) error {
	var _, err = s.pool.Exec(ctx,
		`update devices set last_apply = null where id = $1::uuid`, deviceID)
	return err
}

// UpdatePortTelemetry merges MEASURED port state into inventory.
//
// Telemetry and intent coexist in `devices.ports` — the existing model the facade already
// uses. But they do not mix: this query touches ONLY measured fields (`link`, `speed_mbps`)
// and leaves `mode`, `pvid`, `tagged`, `label` intact. Overwriting intent with a measurement
// would erase configuration an operator just entered.
//
// It also does not touch `desired_changed_at`: a measurement is not an intent change, and
// dating it as such would flash blue on a perfectly conformant fleet.
func (s *Store) UpdatePortTelemetry(ctx context.Context, deviceID string, patch []byte) error {
	var _, err = s.pool.Exec(ctx,
		`update devices
		    set ports = coalesce(ports, '{}'::jsonb) || (
		          select coalesce(jsonb_object_agg(k, coalesce(ports->k, '{}'::jsonb) || v), '{}'::jsonb)
		            from jsonb_each($2::jsonb) as t(k, v)
		        )
		  where id = $1::uuid`, deviceID, patch)
	return err
}

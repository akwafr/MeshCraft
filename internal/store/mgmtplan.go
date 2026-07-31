package store

import (
	"context"
	"errors"
	"net/netip"

	"github.com/jackc/pgx/v5"

	"meshcraft/internal/model"
)

// Site management addressing plan
// (see docs/fr/01-Architecture/Adressage de management).

const mgmtPlanCols = `site_id::text, vlan_id, untagged,
	subnet_v4, gateway_v4, subnet_v6, gateway_v6, dns, ntp,
	source, pool_start, pool_end, strict_plan, updated_at`

func scanMgmtPlan(row pgx.Row) (model.MgmtPlan, error) {
	var p model.MgmtPlan
	var err = row.Scan(&p.SiteID, &p.VlanID, &p.Untagged,
		&p.SubnetV4, &p.GatewayV4, &p.SubnetV6, &p.GatewayV6, &p.DNS, &p.NTP,
		&p.Source, &p.PoolStart, &p.PoolEnd, &p.StrictPlan, &p.UpdatedAt)
	return p, err
}

// GetMgmtPlan returns the site plan. A site without a plan is not an error:
// it returns the default plan (native VLAN 1, external DHCP), meaning
// "meshcraft touches nothing".
func (s *Store) GetMgmtPlan(ctx context.Context, siteID string) (model.MgmtPlan, error) {
	var p, err = scanMgmtPlan(s.pool.QueryRow(ctx,
		`select `+mgmtPlanCols+` from site_mgmt_plan where site_id = $1::uuid`, siteID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.MgmtPlan{
				SiteID: siteID, VlanID: 1, Untagged: true,
				Source: model.MgmtSourceDHCPExternal, StrictPlan: true,
				DNS: []string{}, NTP: []string{},
				Configured: false,
			}, nil
		}
		return p, err
	}
	p.Configured = true
	return p, nil
}

// SaveMgmtPlan writes the plan (create or update).
func (s *Store) SaveMgmtPlan(ctx context.Context, p model.MgmtPlan, updatedBy string) (model.MgmtPlan, error) {
	if p.DNS == nil {
		p.DNS = []string{}
	}
	if p.NTP == nil {
		p.NTP = []string{}
	}
	var out, err = scanMgmtPlan(s.pool.QueryRow(ctx,
		`insert into site_mgmt_plan (site_id, vlan_id, untagged,
		   subnet_v4, gateway_v4, subnet_v6, gateway_v6, dns, ntp,
		   source, pool_start, pool_end, strict_plan, updated_by, updated_at)
		 values ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14::uuid, now())
		 on conflict (site_id) do update set
		   vlan_id = $2, untagged = $3,
		   subnet_v4 = $4, gateway_v4 = $5, subnet_v6 = $6, gateway_v6 = $7,
		   dns = $8, ntp = $9, source = $10, pool_start = $11, pool_end = $12,
		   strict_plan = $13, updated_by = $14::uuid, updated_at = now()
		 returning `+mgmtPlanCols,
		p.SiteID, p.VlanID, p.Untagged,
		p.SubnetV4, p.GatewayV4, p.SubnetV6, p.GatewayV6, p.DNS, p.NTP,
		p.Source, p.PoolStart, p.PoolEnd, p.StrictPlan, updatedBy))
	if err == nil {
		out.Configured = true
	}
	return out, err
}

// MgmtPoolUsage counts pool occupancy. A filling pool must be visible BEFORE
// it is full: that is why the UI shows these three numbers.
func (s *Store) MgmtPoolUsage(ctx context.Context, siteID string) (model.MgmtPoolUsage, error) {
	var u model.MgmtPoolUsage
	var rows, err = s.pool.Query(ctx,
		`select state, count(*) from mgmt_addresses
		 where site_id = $1::uuid and released_at is null group by state`, siteID)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err = rows.Scan(&state, &n); err != nil {
			return u, err
		}
		switch state {
		case "assigned":
			u.Assigned = n
		case "reserved":
			u.Reserved = n
		}
	}
	return u, rows.Err()
}

// PoolSize counts addresses in an inclusive range. Returns 0 if the range is
// empty or inconsistent — the API then says so explicitly rather than showing
// a bogus size.
func PoolSize(start, end string) int {
	var a, errA = netip.ParseAddr(start)
	var b, errB = netip.ParseAddr(end)
	if errA != nil || errB != nil || a.Is4() != b.Is4() {
		return 0
	}
	if b.Less(a) {
		return 0
	}
	// Bounded: a wide IPv6 range makes no sense here, and its cardinality does
	// not fit in an int.
	var n = 0
	var cur = a
	for {
		n++
		if cur == b || n > 1_000_000 {
			break
		}
		cur = cur.Next()
	}
	return n
}

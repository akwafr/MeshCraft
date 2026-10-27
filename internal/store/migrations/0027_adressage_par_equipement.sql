-- The management addressing mode, PER DEVICE.
--
-- What the schema could already say, and what it could not. `site_mgmt_plan.source`
-- (migration 0009) carries the address origin for a whole SITE: "dhcp_external"
-- or "meshcraft". It is a site decision, and it is right at that level — a site's
-- management network does have a dominant regime. But it left no room for the
-- device that does not follow it, and such devices exist: on the reference
-- installation, BOTH CSS326 are on a STATIC address, in the right management
-- subnet, on a site whose plan says "external DHCP".
--
-- What this hole produced: nothing visible, and that is the problem. These
-- devices are perfectly conformant with the plan — their address is in the
-- subnet, they answer — and yet meshcraft could neither say how they had
-- obtained it, nor display the three fields an operator wants to see on a
-- static configuration (address, gateway, DNS), nor record the decision to leave
-- it there. A static device and a device on a DHCP lease looked identical,
-- whereas what happens the day the DHCP server changes has nothing in common
-- between the two.
--
-- # "inherit" is the default, and it means KEEP WHAT THE DEVICE HAS
--
-- Three values: `inherit`, `dhcp`, `static`. The default is `inherit`, and it
-- applies rule 3 of the standard data model — "absent = unmanaged". A device on
-- `inherit` has NOTHING imposed on it: if it is static, it stays static, and that
-- is exactly the request ("if a device is adopted in the right management LAN but
-- with a static IP instead of DHCP, then keep the static configuration"). The
-- EFFECTIVE mode is therefore resolved on read, in three steps: what the device
-- declares it carries first, the site plan's origin next, the cautious default
-- last (see api.effectiveAddrMode).
--
-- The consequence for the shape is what makes this migration risk-free: the whole
-- existing fleet starts on `inherit`, so no behaviour changes, and no row needs to
-- be rewritten. The day an operator wants to switch a device to DHCP, it is an
-- EXPLICIT gesture that sets `dhcp` — not a deduction from the site plan.
--
-- # Why three static columns and not a jsonb
--
-- `mgmt_static_ip`, `mgmt_static_gw`, `mgmt_static_dns` are the three fields the
-- screen shows and the operator types. Storing them in a jsonb would make them
-- unreachable by a query ("which devices point at this gateway?" is the question
-- one will ask the day the gateway changes) and above all impossible to
-- constrain. The jsonb is reserved for what the DEVICE declares
-- (`mgmt_addr_observed`): there, the shape depends on the platform and must not
-- be frozen by a column.
--
-- As `text` and not `inet`/`cidr`, for the reason already written in 0009: these
-- fields are typed in a form, and a strict PostgreSQL type would turn a typo into
-- an unreadable database error where the controller can say "gateway 10.0.0.1
-- does not belong to subnet 192.0.2.0/24". Validation lives in
-- `validateDeviceAddressing`, in a single place, and it is more talkative than a
-- constraint.
--
-- # What these columns are NOT
--
-- It is not `devices.mgmt_ip`, which remains the OBSERVED address — the one at
-- which meshcraft reaches the device, written by relocation and by discovery.
-- Intent and finding keep two distinct columns, as everywhere else in this schema
-- (`ports` versus `observed`, `trap_dest_addr` versus `trap_seen_at`): when the two
-- diverge, it is a drift to DISPLAY, not a value to correct silently.
--
-- Nor is it `devices.mgmt_mode` (keep | site_plan), still absent and still a
-- target: that one will say whether the device enters the site plan, which is a
-- question of VLAN and subnet. The addressing mode says how the address is
-- obtained WITHIN that subnet. The two axes are independent — one can be in the
-- plan with a static address, or outside the plan on DHCP.

alter table devices
    -- inherit: meshcraft imposes nothing and keeps what the device carries.
    add column mgmt_addr_mode  text        not null default 'inherit',
    add column mgmt_static_ip  text        not null default '',
    add column mgmt_static_gw  text        not null default '',
    add column mgmt_static_dns text[]      not null default '{}',
    -- WHO decided, and WHEN. An addressing intent is the kind of decision one re-reads
    -- six months later wondering whether it is still wanted; without an author or a
    -- date, it passes for a factory value.
    add column mgmt_addr_by    uuid references users(id) on delete set null,
    add column mgmt_addr_at    timestamptz,
    -- What the DEVICE declares it carries, as the connector read it. NULL = never
    -- read back, and that is information: we do not pretend to know the mode of a
    -- device we have not queried.
    add column mgmt_addr_observed    jsonb,
    add column mgmt_addr_observed_at timestamptz;

-- The constraint is in the DATABASE and not only in the code, because this column
-- decides what a future write path will set on a device. An unknown value arriving
-- there through a forgotten path — a site-config import, a backup restore — would
-- turn into "unrecognised mode" at the time of the push, i.e. at the worst moment.
alter table devices add constraint devices_mgmt_addr_mode_check
    check (mgmt_addr_mode in ('inherit', 'dhcp', 'static'));

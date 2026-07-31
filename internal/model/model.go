// Package model defines shared types between store and API.
package model

import (
	"encoding/json"
	"time"
)

type User struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	DisplayName   string    `json:"display_name"`
	IsGlobalAdmin bool      `json:"is_global_admin"`
	CreatedAt     time.Time `json:"created_at"`
	Roles         []Role    `json:"roles,omitempty"`
}

type Role struct {
	SiteID   string `json:"site_id"`
	SiteName string `json:"site_name,omitempty"`
	Role     string `json:"role"` // viewer | operator | site_admin
}

// Ordered levels for permission comparisons.
var RoleLevel = map[string]int{"viewer": 1, "operator": 2, "site_admin": 3}

type Site struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
}

type Vlan struct {
	ID      string `json:"id"`
	SiteID  string `json:"site_id"`
	VlanID  int    `json:"vlan_id"`
	Name    string `json:"name"`
	Color   string `json:"color"`
	Purpose string `json:"purpose"`
}

// Profile covers port, device, and switch profiles: same shape, jsonb spec
// follows the standard data model. Tag is switch-only (enrollment via device tag).
type Profile struct {
	ID     string          `json:"id"`
	SiteID string          `json:"site_id"`
	Name   string          `json:"name"`
	Spec   json.RawMessage `json:"spec"`
	Tag    string          `json:"tag,omitempty"`
}

type DeviceGroup struct {
	ID                string  `json:"id"`
	SiteID            string  `json:"site_id"`
	Name              string  `json:"name"`
	DeviceProfileID   *string `json:"device_profile_id"`
	DeviceProfileName *string `json:"device_profile_name,omitempty"`
}

type Device struct {
	ID       string  `json:"id"`
	SiteID   string  `json:"site_id"`
	GroupID  *string `json:"group_id"`
	Name     string  `json:"name"`
	Model    string  `json:"model"`
	Vendor   string  `json:"vendor"`
	Location string  `json:"location"`
	MgmtIP   string  `json:"mgmt_ip"`
	// Status / StatusNote: REACHABILITY as ping watch establishes it. Written by
	// watch ALONE (store.SetDeviceStatus): the API no longer accepts these fields
	// on input, because a form that posts the whole record overwrote observation
	// with the value read when the screen opened.
	Status     string `json:"status"` // ok | warn | off | unknown
	StatusNote string `json:"status_note"`
	// StatusAt: since when the device has been in this state. nil = NEVER observed,
	// which is half the point of the field — "unknown" used to conflate "not pinged
	// yet" and "observation lost in transit".
	//
	// This is NOT a last-seen date: watch only reports transitions, so a device
	// online for three months carries a three-month-old date without anything being
	// late. Reading it as freshness would alert on the healthiest fleet (cf.
	// migration 0025).
	StatusAt   *time.Time      `json:"status_at,omitempty"`
	// SnmpStatus / SnmpStatusNote / SnmpStatusAt: SNMP COLLECTION reachability,
	// established by the proxy's poll transitions (and confirmed by metrics
	// arrival). Distinct from Status, which is ICMP alone — a device can ping
	// while its agent is mute, and the reverse when ICMP is filtered.
	SnmpStatus     string     `json:"snmp_status"` // ok | off | unknown
	SnmpStatusNote string     `json:"snmp_status_note"`
	SnmpStatusAt   *time.Time `json:"snmp_status_at,omitempty"`
	PoEBudgetW int             `json:"poe_budget_w"`
	PosX       float64         `json:"pos_x"`
	PosY       float64         `json:"pos_y"`
	PortGroups json.RawMessage `json:"port_groups"`
	Ports      json.RawMessage `json:"ports"`
	Extra      json.RawMessage `json:"extra"`

	// Observed: configuration ACTUALLY read on the device, as opposed to `Ports`
	// which carries what meshcraft wants. Conflating them would never let you say
	// which of the two is behind (cf. docs/01-Architecture/Reconciliation).
	Observed json.RawMessage `json:"observed,omitempty"`
	// ObservedAt: nil = never re-read. We do not pretend to know.
	ObservedAt *time.Time `json:"observed_at,omitempty"`
	// DesiredChangedAt / AppliedAt: enough to ATTRIBUTE a gap without guessing. If
	// intent has not moved since apply and the device still differs, the device
	// changed.
	DesiredChangedAt *time.Time `json:"desired_changed_at,omitempty"`
	AppliedAt        *time.Time `json:"applied_at,omitempty"`
	// LastApply: what the last apply did, and especially what it deliberately left
	// aside. Without this trace, a protective guardrail looks like a bug.
	LastApply json.RawMessage `json:"last_apply,omitempty"`
	// Sync and Drift are COMPUTED on read, never stored: a frozen sync state in the
	// database would lie on the next change.
	Sync  string `json:"sync,omitempty"`
	Drift []any  `json:"drift,omitempty"`
	// ApplyStalled: an apply ALREADY happened since the last change, yet gaps remain.
	//
	// Distinguishes the two "in progress" states the blue indicator conflates. Intent
	// just entered will be applied within seconds: nothing to signal. Intent an apply
	// already ran through while leaving gaps will never move on its own — meshcraft
	// only re-applies after a change, and there will be none. Without this
	// distinction, a half-configured device is indistinguishable from one being
	// configured, and shows nothing.
	ApplyStalled bool `json:"apply_stalled,omitempty"`
	// WatchExcluded: NO proxy watches this device, and here is why.
	//
	// Refusing to watch an ambiguous address is the right tradeoff — a false state
	// is worse than no state. But while it lived only in controller logs, it produced
	// exactly what it tried to avoid: a device with no indicator, no explanation,
	// looking like a meshcraft failure. Computed on read, never stored.
	WatchExcluded string `json:"watch_excluded,omitempty"`
	// AddressDomainID: the network in which this device's management address makes
	// sense (cf. internal/netscope). Empty for a manually entered device that never
	// declared one — it stays watched while alone on its address, and loses
	// arbitration to a device whose domain is declared.
	AddressDomainID string `json:"address_domain_id,omitempty"`
	// ProxyID / ProxyName: this device's REFERENCE COLLECTOR — the proxy that polls
	// it via SNMP, and whose address will be written into the device as trap
	// destination (cf. migration 0022). Empty = no attachment, eligibility falls
	// back to site coverage.
	//
	// The name accompanies the ID, as everywhere else (ScanPrefix, Sighting,
	// Pipeline): a list of UUIDs is unreadable, and "collected by" is exactly the
	// kind of information you want to display without reloading the proxy list to
	// decode it.
	ProxyID   string `json:"proxy_id,omitempty"`
	ProxyName string `json:"proxy_name,omitempty"`
	// TrapDestAddr / TrapDestAt: the address meshcraft WROTE into the device as
	// trap recipient, and when (cf. migration 0023). Empty = nothing was ever
	// applied.
	//
	// This is a CLAIM: the device does carry the line (the connector verifies by
	// re-read), but the address comes from the collector proxy routing table and
	// remains a hypothesis about the RETURN path. A firewall or ACL on the management
	// VLAN, and the device emits into the void while nothing here changes.
	TrapDestAddr string `json:"trap_dest_addr,omitempty"`
	// TrapDestProxyID: which proxy that address came from. Travels with the address
	// and is not decorative — it makes the claim REFUTABLE: an address without its
	// owner does not say when to stop believing it.
	TrapDestProxyID string     `json:"trap_dest_proxy_id,omitempty"`
	TrapDestAt      *time.Time `json:"trap_dest_at,omitempty"`
	// TrapSeenAt: when the CONTROLLER recorded the first trap from this device
	// accepted by the proxy whose address was written. This is PROOF, and it is not
	// negotiable — the packet crossed.
	//
	// "When the controller recorded", not "when the trap arrived": the nuance cost a
	// bug. The date comes from `now()` in the database, therefore the same clock as
	// TrapDestAt, because these two columns exist to be compared. Timestamp set by
	// the proxy — a machine at a customer site, with no sync guarantee — was
	// measured eleven seconds late on a real installation: stored here, it made proof
	// earlier than what it proved. It now serves only to log the gap.
	//
	// Two fields not one state, so "address written" and "return path established"
	// can never be conflated: a record that merged them would suggest reactive
	// monitoring that does not exist. Reset to nil when destination changes (cf.
	// store.SetDeviceProxy): proof is about a path, not a device.
	TrapSeenAt *time.Time `json:"trap_seen_at,omitempty"`
	// MgmtAddr: this device's management addressing INTENT (migration 0027). Always
	// present — a never-decided device carries `mode: "inherit"`, which is an answer
	// not an absence.
	MgmtAddr DeviceAddressing `json:"mgmt_addr"`
	// MgmtAddrObserved: what the DEVICE declares it carries, as a connector read it
	// (driver.MgmtAddrState). Empty = never re-read, and we do not pretend to know
	// the mode of a device we never queried.
	//
	// Separate from `Observed`, which carries VLANs and ports, for two reasons of
	// different nature. First is platform fact: management addressing is read on
	// different pages than port config (/sys.b vs /vlan.b and /fwd.b, `ip/address`
	// vs `interface/bridge/vlan`), so one read can succeed when the other fails.
	// Second is product fact: `Observed` is compared to intent to compute port drift,
	// and pouring addressing into it would pull management into a calculation that
	// triggers applies.
	MgmtAddrObserved   json.RawMessage `json:"mgmt_addr_observed,omitempty"`
	MgmtAddrObservedAt *time.Time      `json:"mgmt_addr_observed_at,omitempty"`
}

type DeviceLink struct {
	ID      string `json:"id"`
	SiteID  string `json:"site_id"`
	ADevice string `json:"a_device"`
	APort   string `json:"a_port"`
	BDevice string `json:"b_device"`
	BPort   string `json:"b_port"`
	Speed   string `json:"speed"`
	Status  string `json:"status"` // up | warn | down
	Note    string `json:"note"`
}

// --- LLDP neighborhood: OBSERVATION, beside intent above ---
//
// See docs/01-Architecture/Voisinage LLDP.md and migration 0028, which carry the
// observation and reasoning. A `DeviceLink` is what an operator DECLARED and that
// survives unplugging; what follows is what a device SAW thirty seconds ago.
// Conflating them would erase a entered link because a switch was unplugged ten
// seconds ago.

// LLDPNeighbor is ONE dated observation: this port saw that neighbor.
type LLDPNeighbor struct {
	DeviceID string `json:"device_id"`
	SiteID   string `json:"site_id"`
	// LocalPort: FACADE port name, already resolved by the controller (`resolve`). A
	// label we could not attach never reaches here.
	LocalPort    string `json:"local_port"`
	LocalPortNum int    `json:"local_port_num,omitempty"`
	// ChassisID / PortID / PortDesc / SysName / SysDesc: what the neighbor said
	// about itself, AS IS. Normalization is a matching decision (`normalizeChassisID`),
	// not a stored value: an operator searching why a neighbor was not recognized
	// must be able to read what the device actually answered.
	ChassisID string `json:"chassis_id,omitempty"`
	PortID    string `json:"port_id,omitempty"`
	PortDesc  string `json:"port_desc,omitempty"`
	SysName   string `json:"sys_name,omitempty"`
	SysDesc   string `json:"sys_desc,omitempty"`
	// FirstSeen: since when this neighbor has been seen on this port WITHOUT
	// interruption. A neighbor that disappears then returns resets to zero — the
	// cable moved, prior observation age means nothing.
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// PeerDevice / PeerPort: fleet device recognized behind this neighbor, and its
	// port. EMPTY when we do not recognize it, which is the ORDINARY case — a
	// provider router, an access point outside meshcraft. COMPUTED on read, never
	// stored: matching depends on inventory, which moves, and a frozen ID in the
	// database would lie on the next adoption of the device on the other side.
	PeerDevice string `json:"peer_device,omitempty"`
	PeerPort   string `json:"peer_port,omitempty"`
	// PeerVia says WHAT the match rested on — chassis, port ID, or system name — or
	// stays empty if none occurred.
	//
	// Not decoration. The three do not have equal strength: a chassis is a MAC,
	// therefore globally unique; a system name is a string an operator typed and two
	// devices may share. A link traced on a name deserves to be read as less certain,
	// and hiding that would present both as equivalent.
	PeerVia string `json:"peer_via,omitempty"`
}

// LLDPSelf is the LLDP identity a device ANNOUNCES — its business card, as opposed
// to what it sees.
type LLDPSelf struct {
	DeviceID  string `json:"device_id"`
	SiteID    string `json:"site_id"`
	ChassisID string `json:"chassis_id,omitempty"`
	SysName   string `json:"sys_name,omitempty"`
	// PortIDs: this device's `lldpLocPortId` values. See migration 0028: this field
	// catches a vendor that announces the port MAC as chassis-id, without which the
	// reference fleet's only bilateral link would present as seen from one side only.
	PortIDs []string `json:"port_ids,omitempty"`
	// Served: does this device serve the LLDP-MIB? False != "zero neighbors".
	Served bool      `json:"served"`
	SeenAt time.Time `json:"seen_at"`
}

// LLDPLink is a link DERIVED from one or two observations. Never stored: computed
// on read by `api.deriveLLDPLinks`.
type LLDPLink struct {
	// ADevice / APort: the end that SAW. Always a fleet device.
	ADevice string `json:"a_device"`
	APort   string `json:"a_port"`
	// BDevice / BPort: the SEEN end, when we recognize it. Empty `BDevice` = a
	// neighbor whose chassis is in no fleet device: the link is still drawn, and
	// that is the most common operations case.
	BDevice string `json:"b_device,omitempty"`
	BPort   string `json:"b_port,omitempty"`
	// BLabel / BPortLabel: what the neighbor announced, to display when we do not
	// recognize it — and even when we do, because "lan-r4" explains why meshcraft
	// matched this observation to `lab-crs326`.
	BLabel     string `json:"b_label,omitempty"`
	BPortLabel string `json:"b_port_label,omitempty"`
	BChassisID string `json:"b_chassis_id,omitempty"`
	BSysDesc   string `json:"b_sys_desc,omitempty"`
	// BothSides: both ends see each other MUTUALLY.
	//
	// False is the NORMAL case, not an anomaly, and must be stated that way: an
	// entire platform in the reference fleet does not speak 802.1AB (SwOS), so a
	// link touching it will never be seen from both sides. A one-sided link is still
	// drawn, and stated as such.
	BothSides bool `json:"both_sides"`
	// FirstSeen / LastSeen: observation dates. When the link is seen from both
	// sides, `FirstSeen` is the OLDER of the two and `LastSeen` the most RECENT — the
	// link exists since the first side saw it, and was confirmed at the later of the
	// two snapshots.
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// Event log severity levels.
const (
	SevDebug    = 0
	SevInfo     = 1
	SevNotice   = 2
	SevWarning  = 3
	SevError    = 4
	SevCritical = 5
)

type Event struct {
	ID       int64           `json:"id"`
	TS       time.Time       `json:"ts"`
	SiteID   *string         `json:"site_id"`
	DeviceID *string         `json:"device_id"`
	PortName string          `json:"port_name"`
	Type     string          `json:"type"`
	Severity int             `json:"severity"`
	Message  string          `json:"message"`
	Actor    string          `json:"actor"`
	Payload  json.RawMessage `json:"payload"`
}

// Origin of a site's management address.
//
//	MgmtSourceDHCPExternal: meshcraft configures the DHCP client and allocates nothing.
//	MgmtSourceMeshcraft:    meshcraft ALLOCATES from the pool and pushes a STATIC
//	                        configuration. Not a DHCP server — see docs/01-Architecture/Adressage de management.
const (
	MgmtSourceDHCPExternal = "dhcp_external"
	MgmtSourceMeshcraft    = "meshcraft"
)

// Management addressing mode for ONE device (migration 0027).
//
// Do not confuse with MgmtSource*, which is address origin for an entire SITE. The
// site plan states the dominant management network regime; these three values say
// what THIS device does, which is not always in the dominant regime — both CSS326
// units in the reference installation are static in a site whose plan says "external
// DHCP".
//
//	MgmtAddrInherit: meshcraft imposes NOTHING. Default, and applies "absent =
//	                 unmanaged": a static device stays static. Effective mode
//	                 resolves on read — what the device declares first, plan origin
//	                 second.
//	MgmtAddrDHCP:    operator wants this device as a DHCP client.
//	MgmtAddrStatic:  operator wants this device at a fixed address, and the three
//	                 accompanying fields are authoritative.
const (
	MgmtAddrInherit = "inherit"
	MgmtAddrDHCP    = "dhcp"
	MgmtAddrStatic  = "static"
)

// DeviceAddressing is a device's management addressing INTENT.
//
// Grouped in one structure not scattered on Device: written in one gesture by a
// dedicated endpoint, exactly for the reason that moved `status` out of UpdateDevice
// — a form posting the whole record must not rewrite management addressing with
// the value read when the screen opened. On this data, the error costs device
// access.
type DeviceAddressing struct {
	// Mode: inherit | dhcp | static.
	Mode string `json:"mode"`
	// StaticIP carries the PREFIX ("10.99.0.36/24") not just the address.
	//
	// Prefix is mandatory in what is STORED, never in what is entered: the API
	// completes it from the site plan subnet when the operator types a bare address,
	// and REFUSES when no plan provides it. Guessing /24 would invent a mask, and a
	// wrong mask makes the device silent as surely as a wrong address.
	StaticIP string `json:"static_ip,omitempty"`
	// StaticGateway may stay empty, including in static mode: a layer-2 switch
	// managed from its own subnet needs none, and some platforms simply do not
	// carry one (SwOS — cf. MgmtCapabilities.Gateway).
	StaticGateway string   `json:"static_gateway,omitempty"`
	StaticDNS     []string `json:"static_dns,omitempty"`
	// By / At: who decided, and when. Nil = nobody ever decided, device is in
	// inherited default.
	By *string    `json:"by,omitempty"`
	At *time.Time `json:"at,omitempty"`
}

// MgmtPlan: a site's management addressing plan.
type MgmtPlan struct {
	SiteID string `json:"site_id"`
	// VlanID / Untagged: admin VLAN. Untagged = native VLAN, the only mode possible
	// on some devices.
	VlanID    int      `json:"vlan_id"`
	Untagged  bool     `json:"untagged"`
	SubnetV4  string   `json:"subnet_v4"`
	GatewayV4 string   `json:"gateway_v4"`
	SubnetV6  string   `json:"subnet_v6"`
	GatewayV6 string   `json:"gateway_v6"`
	DNS       []string `json:"dns"`
	NTP       []string `json:"ntp"`
	Source    string   `json:"source"`
	PoolStart string   `json:"pool_start"`
	PoolEnd   string   `json:"pool_end"`
	// StrictPlan: refuse adoption of a device unable to carry this plan, rather than
	// accepting it off-plan.
	StrictPlan bool      `json:"strict_plan"`
	UpdatedAt  time.Time `json:"updated_at"`
	// Configured says whether the site has an explicit plan or inherits default.
	Configured bool `json:"configured"`
}

// MgmtPoolUsage: allocation pool occupancy.
type MgmtPoolUsage struct {
	Size     int `json:"size"`
	Assigned int `json:"assigned"`
	Reserved int `json:"reserved"`
	Free     int `json:"free"`
}

// SiteCredential: credential used on a site's devices.
//
// One per site (empty group), plus one per device group when model password
// policies are incompatible. Secret is never returned in list — only HasSecret
// says so.
type SiteCredential struct {
	ID        string `json:"id"`
	SiteID    string `json:"site_id"`
	GroupName string `json:"group_name"`
	Kind      string `json:"kind"`
	Username  string `json:"username"`
	HasSecret bool   `json:"has_secret"`
	// HasSecret2: second secret phrase is set. Specific to `snmp_v3`, which derives
	// TWO keys — authentication and encryption — and both derivations would receive
	// the same input if one phrase fed both (cf. migration 0017). Like HasSecret,
	// this is PRESENCE: list never returns a secret, only reveal does, and that is
	// audited.
	HasSecret2 bool   `json:"has_secret2"`
	Note       string `json:"note"`
	// RotatedAt: a password unchanged for two years should be visible.
	RotatedAt time.Time `json:"rotated_at"`
	CreatedAt time.Time `json:"created_at"`
	// PerDevice: distinct secret per device instead of shared secret.
	PerDevice bool `json:"per_device"`
	// Tag: credential applies only to devices carrying this tag. Empty = no tag
	// scope (site default, or group scope). Cf. docs/01-Architecture/Etiquettes et portee des identifiants.
	Tag string `json:"tag"`
	// Priority breaks ties between two tag credentials targeting the same device:
	// highest wins.
	Priority int `json:"priority"`
	// Generations: history, most recent first.
	Generations []CredentialGeneration `json:"generations,omitempty"`
}

// CredentialGeneration: one secret version. APPEND-ONLY — never overwrite, always
// add (cf. docs/03-Security/Identite de service et rotation section 3).
type CredentialGeneration struct {
	ID           string `json:"id"`
	CredentialID string `json:"credential_id"`
	// Empty DeviceID = shared secret for the scope.
	DeviceID   string `json:"device_id,omitempty"`
	Generation int    `json:"generation"`
	// State: pending | active | retiring | retired | compromised.
	// `retiring` means REPLACED BUT STILL VALID: devices still carry it, and it is
	// the only way to connect to them.
	State       string     `json:"state"`
	CreatedAt   time.Time  `json:"created_at"`
	ActivatedAt *time.Time `json:"activated_at"`
	RetiredAt   *time.Time `json:"retired_at"`
	// InstalledOn: how many devices carry this generation. That count forbids purge.
	InstalledOn int `json:"installed_on"`
}

// WatchedDevice: what a proxy needs to watch a device — nothing more than a name
// and an address.
type WatchedDevice struct {
	ID     string `json:"id"`
	SiteID string `json:"-"`
	Name   string `json:"name"`
	MgmtIP string `json:"mgmt_ip"`
	// AddressDomainID: network in which this address makes sense. Empty for manual
	// entry that never declared one. Does NOT go to the proxy: controller uses it to
	// decide which proxy is legitimate for this address (cf.
	// internal/netscope.SelectWatchTargets).
	AddressDomainID string `json:"-"`

	// --- Reference collector attachment (migration 0022) ---
	//
	// These three fields do not go to the proxy either: controller uses them to
	// decide what it asks THIS proxy, and there is nothing to say on the proxy side
	// — a proxy that receives a target watches it, period.

	// ProxyID / ProxyName: DECLARED reference collector for this device, regardless
	// of which proxy this list is destined for. Empty = no attachment. Name is for
	// operator messages: "collected by proxy-akwalan" reads; a UUID does not.
	ProxyID   string `json:"-"`
	ProxyName string `json:"-"`

	// Collector: is the proxy this list is destined for the reference collector
	// for THIS device?
	//
	// This field carries all attachment arbitration, and lives here rather than in a
	// second store method for a precise reason: the returned list serves TWO uses
	// that want different things.
	//
	//	ICMP  — every row, attached or not. Two proxies pinging the same device cost
	//	        nothing and give better reachability truth; restricting ping to the
	//	        collector would lose all failover the day that proxy falls.
	//	SNMP  — rows where Collector is true, and only those. A trap destination is
	//	        ONE IP address: "several eligible proxies" does not translate there.
	//	        And two proxies polling the same switch report every port transition
	//	        twice.
	//
	// One query, one truth, caller decides according to what it builds. Two store
	// methods would mean two database passes on every proxy connection for the same
	// table row, and worse two lists that can diverge in between: ICMP target and
	// SNMP target for one device would come from different moments.
	//
	// True for an UNATTACHED device: with nobody declaring a collector, every
	// eligible proxy is one — exactly pre-migration 0022 behavior, preserved
	// entirely.
	Collector bool `json:"-"`
}

// AddressDomain: the space in which one IP address designates one machine and only
// one — see migration 0020 and internal/netscope.
//
// An operator DECLARATION, never a deduction. Two proxies attached to the same
// domain watch the same network, and their observations merge; attached to two
// domains, they watch two foreign networks that happen to share an addressing plan,
// and nothing mixes.
type AddressDomain struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Note string `json:"note"`
	// Prefixes / Proxies: enough to show, in the UI, what this domain actually covers.
	// A domain whose contents you cannot see is one more concept, not a tool.
	Prefixes  int       `json:"prefixes"`
	Proxies   []string  `json:"proxies"`
	CreatedAt time.Time `json:"created_at"`
}

// SiteRef identifies a site without loading full detail.
type SiteRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Proxy: network presence point. Its COVERAGE governs who sees it, who administers
// it, and for which sites it may serve — see docs/01-Architecture/Proxy.
//
//	Global = true  -> instance proxy, usable everywhere (global-admin).
//	Global = false -> restricted to Sites. Empty Sites = covers NOTHING, never
//	                 "everywhere": otherwise removing the last site would promote
//	                 the proxy to global.
//
// Coverage does not say what the proxy can reach (observed in device_paths), nor
// the site of devices it discovers (chosen at adoption).
type Proxy struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Version: UTC build timestamp YYYYMMDD.HHMMSS announced by the proxy. Empty =
	// not yet seen since switching to this format.
	Version   string     `json:"version"`
	Protocol  int        `json:"protocol"`
	OS        string     `json:"os,omitempty"`
	Arch      string     `json:"arch,omitempty"`
	Global    bool       `json:"global"`
	Sites     []SiteRef  `json:"sites"`
	Internal  bool       `json:"internal"`
	Revoked   bool       `json:"revoked"`
	CreatedAt time.Time  `json:"created_at"`
	LastSeen  *time.Time `json:"last_seen"`

	// Upgrade (cf. docs/01-Architecture/Versionnage et mise a jour du proxy).
	AutoUpgrade      bool       `json:"auto_upgrade"`
	PinVersion       string     `json:"pin_version"`
	UpgradeRequested bool       `json:"upgrade_requested"`
	UpgradeState     string     `json:"upgrade_state"`
	UpgradeVersion   string     `json:"upgrade_version"`
	UpgradeError     string     `json:"upgrade_error"`
	UpgradeStartedAt *time.Time `json:"upgrade_started_at"`
	UpgradedAt       *time.Time `json:"upgraded_at"`

	MigrateURL string     `json:"migrate_url"`
	MigratedAt *time.Time `json:"migrated_at"`
}

// KnownCredential: instance "known standard" credential, tried by discovery. Secret
// is never returned by the API — only HasSecret says so.
type KnownCredential struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"` // ssh | telnet | http
	Username  string    `json:"username"`
	Priority  int       `json:"priority"`
	Enabled   bool      `json:"enabled"`
	HasSecret bool      `json:"has_secret"`
	CreatedAt time.Time `json:"created_at"`
}

// ScanPrefix: IPv4/IPv6 range to scan, declared ON A PROXY (never on a site: at
// discovery time, site is not yet known).
//
// Proxy says HOW you reach the range; address domain says WHICH network it is. Two
// different questions, and conflating them is precisely what made `192.168.1.0/24`
// ambiguous once it appeared on two proxies.
type ScanPrefix struct {
	ID              string     `json:"id"`
	ProxyID         string     `json:"proxy_id"`
	ProxyName       string     `json:"proxy_name,omitempty"`
	AddressDomainID string     `json:"address_domain_id"`
	DomainName      string     `json:"domain_name,omitempty"`
	Prefix          string     `json:"prefix"`
	Enabled         bool       `json:"enabled"`
	Note            string     `json:"note"`
	LastRunAt       *time.Time `json:"last_run_at"`
	LastResult      string     `json:"last_result"`
	CreatedAt       time.Time  `json:"created_at"`
}

// Sighting: a proxy saw this candidate at this address. Several proxies for one
// candidate is normal, not a conflict.
type Sighting struct {
	ProxyID   string    `json:"proxy_id"`
	ProxyName string    `json:"proxy_name"`
	IP        string    `json:"ip"`
	LatencyMS int       `json:"latency_ms"`
	LastSeen  time.Time `json:"last_seen"`
}

// Discovered: discovery candidate. NO SITE by construction — site is chosen at
// adoption (cf. docs/01-Architecture/Discovery).
type Discovered struct {
	ID        string `json:"id"`
	Source    string `json:"source"` // scan | manual | serial
	ProxyID   string `json:"proxy_id"`
	ProxyName string `json:"proxy_name"`
	// AddressDomainID / DomainName: network in which the address below makes sense.
	// It, not the proxy, qualifies the IP in the merge key — two proxies on one
	// network must produce one candidate.
	AddressDomainID string          `json:"address_domain_id"`
	DomainName      string          `json:"domain_name,omitempty"`
	IP              string          `json:"ip"`
	MAC             string          `json:"mac"`
	Serial          string          `json:"serial"`
	Hostname        string          `json:"hostname"`
	Vendor          string          `json:"vendor"`
	Model           string          `json:"model"`
	Driver          string          `json:"driver"`
	Confidence      int             `json:"confidence"`
	Transports      string          `json:"transports"`
	Fingerprint     json.RawMessage `json:"fingerprint"`
	// HasConfig: configuration was read and can be imported at adoption. Text itself
	// is returned only by the detail route.
	HasConfig     bool       `json:"has_config"`
	CredentialID  *string    `json:"credential_id"`
	Note          string     `json:"note"`
	FirstSeen     time.Time  `json:"first_seen"`
	LastSeen      time.Time  `json:"last_seen"`
	Sightings     []Sighting `json:"sightings"`
	AdoptedDevice *string    `json:"adopted_device_id"`
	AdoptedSite   *string    `json:"adopted_site_id"`
	AdoptedAt     *time.Time `json:"adopted_at"`
}

// EnrollToken: single use, short TTL. Carries the COVERAGE the proxy will take on
// enrollment — frozen at issuance, because emitter rights may change before the
// token is consumed.
type EnrollToken struct {
	ID        string     `json:"id"`
	Global    bool       `json:"global"`
	SiteIDs   []string   `json:"site_ids"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at"`
}

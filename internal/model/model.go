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
	// LocalDomain is the DNS domain this site's equipment belongs to.
	//
	// It used to be a constant compiled into the Cisco connector
	// (`meshcraft.local`), pushed onto every switch of every site — and it is not
	// an internal detail: the device carries it in its own FQDN afterwards, which
	// the lab's LLDP neighbours show verbatim ("CiscoLabFirst1.meshcraft.local").
	// A site that already has a domain was getting equipment announcing one that
	// resolves nowhere.
	//
	// EMPTY IS LEGITIMATE and means "this site has no domain". Connectors that
	// require one — IOS, whose "crypto key generate rsa" refuses without it —
	// fall back to their own default rather than writing an empty line, which
	// would make SSH silently impossible.
	LocalDomain string `json:"local_domain"`
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
	StatusAt *time.Time `json:"status_at,omitempty"`
	// SnmpStatus / SnmpStatusNote / SnmpStatusAt: SNMP COLLECTION reachability,
	// established by the proxy's poll transitions (and confirmed by metrics
	// arrival). Distinct from Status, which is ICMP alone — a device can ping
	// while its agent is mute, and the reverse when ICMP is filtered.
	SnmpStatus     string          `json:"snmp_status"` // ok | off | unknown
	SnmpStatusNote string          `json:"snmp_status_note"`
	SnmpStatusAt   *time.Time      `json:"snmp_status_at,omitempty"`
	PoEBudgetW     int             `json:"poe_budget_w"`
	PosX           float64         `json:"pos_x"`
	PosY           float64         `json:"pos_y"`
	PortGroups     json.RawMessage `json:"port_groups"`
	Ports          json.RawMessage `json:"ports"`
	Extra          json.RawMessage `json:"extra"`

	// Observed: configuration ACTUALLY read on the device, as opposed to `Ports`
	// which carries what meshcraft wants. Conflating them would never let you say
	// which of the two is behind (cf. docs/en/01-Architecture/Reconciliation.md).
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
	// ModelKey / ModelLabel / ModelSKU: the CANONICAL IDENTITY of the hardware,
	// derived from `Model` on read and never stored (cf. ADR-0014 and
	// internal/modelid).
	//
	// `Model` stays exactly as the device — or an operator — declared it, because
	// that string is a piece of evidence: the day a faceplate is wrong, the first
	// question is which naming grammar arrived, and it only has an answer while
	// the raw label is intact. What was missing is not a reading but an identity:
	// one vendor publishing two grammars for one switch put the same twelve
	// machines in the inventory under three different model strings.
	//
	// `ModelLabel` is what a screen must NAME the device — the canonical form
	// when a family rule recognized the label, the raw string otherwise. It is
	// never a shortened form: fitting a label into a tile is a different job, and
	// web/src/modelLabel.ts does it over this value.
	//
	// Derived rather than stored for the reason every field on this shelf is: a
	// persisted copy would freeze the derivation of the day it was written, while
	// a computed one improves for every existing row the day it learns one more
	// grammar.
	ModelKey   string `json:"model_key,omitempty"`
	ModelLabel string `json:"model_label,omitempty"`
	ModelSKU   string `json:"model_sku,omitempty"`
	// ApplyStalled: an apply ALREADY happened since the last change, yet gaps remain.
	//
	// Distinguishes the two "in progress" states the blue indicator conflates. Intent
	// just entered will be applied within seconds: nothing to signal. Intent an apply
	// already ran through while leaving gaps will never move on its own — meshcraft
	// only re-applies after a change, and there will be none. Without this
	// distinction, a half-configured device is indistinguishable from one being
	// configured, and shows nothing.
	ApplyStalled bool `json:"apply_stalled,omitempty"`
	// Applying: an automatic apply is RUNNING on this device RIGHT NOW.
	//
	// Computed, never stored, and true only while the connector is actually writing
	// to the device. It exists because "pending" conflates two very different
	// moments: "queued, nothing happening yet" and "being provisioned on the wire".
	// A template pushed via a tag applies in the background after a short debounce
	// and can take tens of seconds over SSH; without this flag the screen shows a
	// still blue dot and no port movement, so nothing tells the operator work is
	// under way. The frontend turns this into a live "provisioning" animation.
	Applying bool `json:"applying,omitempty"`
	// WatchExcluded: NO proxy watches this device, and here is why.
	//
	// Refusing to watch an ambiguous address is the right tradeoff — a false state
	// is worse than no state. But while it lived only in controller logs, it produced
	// exactly what it tried to avoid: a device with no indicator, no explanation,
	// looking like a meshcraft failure. Computed on read, never stored.
	WatchExcluded string `json:"watch_excluded,omitempty"`
	// CollectKind: WHICH PROTOCOL collects this device — "snmp" or "gnmi".
	//
	// Computed on read from the connector's declared monitoring channels, never
	// stored: a device's collector changes when its connector's declaration
	// does, and a persisted copy would keep naming the old one.
	//
	// It exists because `SnmpStatus` is misnamed for half its meaning. That field
	// carries COLLECTION reachability whatever protocol collects — the proxy's
	// gNMI supervisor deliberately reuses the same up/down events, so that a
	// screen asking "is this device being collected" gets one answer rather than
	// two. What it cannot do is say which protocol was tried, and a pill reading
	// "snmp" on an Arista is simply false: that platform declares no SNMP
	// channel at all and nothing in the product has ever polled it.
	//
	// Renaming the status field itself would be the tidier fix and is not worth
	// it: `snmp_status` is in the API, in the frontend, and in the stored device
	// rows, and the rename would touch all three to change a label.
	CollectKind string `json:"collect_kind,omitempty"`
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
// See docs/en/01-Architecture/LLDP neighbours.md and migration 0028, which carry
// the observation and reasoning. A `DeviceLink` is what an operator DECLARED and
// that survives unplugging; what follows is what a device SAW thirty seconds
// ago. Conflating them would erase an entered link because a switch was
// unplugged ten seconds ago.

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
	// Refuted / RefutedBy: this observation claims a remote port that somebody ELSE
	// demonstrably occupies in a STRICTLY fresher observation, so it no longer
	// produces a topology edge. RefutedBy names the device now found on that far
	// port. See `api.markRefutedObservations` for the rule and its four refusals.
	//
	// COMPUTED on read, never stored — same reason as PeerDevice/PeerPort above, and
	// here it is even stronger: the verdict depends on the OTHER device's next
	// snapshot, so freezing it in the table would keep contradicting an observation
	// the far end has already withdrawn. The row itself stays listed, because the
	// device really did report that neighbor; only the derived link disappears.
	Refuted   bool   `json:"refuted,omitempty"`
	RefutedBy string `json:"refuted_by,omitempty"`
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
	// Source: which collection path wrote this snapshot — "snmp" (the proxy's
	// periodic LLDP-MIB walk) or "cli" (the controller's read-state gesture,
	// over the admin session). See migration 0037: without this word, the
	// poller's next EMPTY walk of an agent that serves no LLDP-MIB would
	// silently erase what the CLI just observed, thirty seconds after every
	// read-state — the FS S3400 case, measured.
	Source string `json:"source,omitempty"`
}

// MACSighting is one row of a device's CURRENT forwarding table: this bridge,
// at last_seen, still testified that this MAC lives behind this port in this
// VLAN. Replaced snapshot by snapshot, like LLDPNeighbor; the durable per-MAC
// record is [Client].
type MACSighting struct {
	DeviceID string `json:"device_id"`
	SiteID   string `json:"site_id"`
	// Port: FACADE name, resolved at ingest — same rule as LLDPNeighbor.LocalPort.
	Port string `json:"port"`
	// VLAN: 0 = the platform did not say (flat BRIDGE-MIB), never "VLAN 0".
	VLAN int `json:"vlan"`
	// MAC in canonical spelling: lowercase colon-separated hex.
	MAC       string    `json:"mac"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// Client is the durable per-MAC ledger of a site: identity plus the bandwidth
// bucket that survives unplugs and reboots and re-attaches when the FDB shows
// the MAC again (the "waiting for rediscovery" the Clients page promises).
type Client struct {
	SiteID    string    `json:"site_id"`
	MAC       string    `json:"mac"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// RxBytes: toward the client (its port's egress). TxBytes: from it (the
	// port's ingress).
	RxBytes  int64      `json:"rx_bytes"`
	TxBytes  int64      `json:"tx_bytes"`
	BucketAt *time.Time `json:"bucket_at,omitempty"`
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
//	                        configuration. Not a DHCP server — see docs/en/01-Architecture/Management addressing.md.
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
	// StaticIP carries the PREFIX ("192.0.2.36/24") not just the address.
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
	// scope (site default, or group scope). See docs/en/01-Architecture/Tags and credential scope.md.
	Tag string `json:"tag"`
	// Priority breaks ties between two tag credentials targeting the same device:
	// highest wins.
	Priority int `json:"priority"`
	// TransportTLS: how a collector connecting WITH this credential protects it on
	// the wire. `verify` | `skip-verify` | `plaintext`, and `verify` is the
	// default (migration 0039).
	//
	// Read only by the `gnmi` kind today, because it is the only one that carries
	// a DEVICE LOGIN over a connection whose encryption is optional. An SNMP
	// community has no TLS to configure, and an `admin` password rides SSH, which
	// is never in the clear.
	//
	// Three values rather than two booleans so that "plaintext and skip-verify"
	// — which means nothing — cannot be written down. See the migration for why
	// `skip-verify` exists at all rather than being refused: an unverified TLS
	// session still defeats a passive listener, and refusing it outright would
	// push a site with no CA to `plaintext`, which defeats nothing.
	TransportTLS string `json:"transport_tls,omitempty"`
	// CAPEM: the authority the target's certificate must chain to, PEM-encoded.
	//
	// Not a secret and deliberately not in the vault: a CA certificate is public
	// by construction — distributing it is the point. Empty means the system
	// pool, which on a proxy image holds no switch's issuer.
	CAPEM string `json:"ca_pem,omitempty"`
	// Generations: history, most recent first.
	Generations []CredentialGeneration `json:"generations,omitempty"`
}

// CredentialGeneration: one secret version. APPEND-ONLY — never overwrite, always
// add (see docs/en/03-Security/Service identity and rotation.md section 3).
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
// ProxyReach is ONE proxy's observation of one device (migration 0035).
//
// Reachability stopped being a single column the day a proxy that could not reach
// a device overwrote the verdict of one that could. Each proxy now keeps its own,
// and [ProxyReach.ConfirmedAt] is what decides whose counts: a proxy that has
// matched sysDescr/sysObjectID against this device's vendor and model has PROVEN
// it is talking to the right equipment, and its verdict outranks any other.
type ProxyReach struct {
	ProxyID   string `json:"proxy_id"`
	ProxyName string `json:"proxy_name,omitempty"`
	// Reachable is this proxy's own verdict. NIL means it watches the device but
	// has not concluded yet — distinct from "it says down", and aggregating the
	// two as one would report an outage the instant a proxy connects.
	Reachable   *bool     `json:"reachable"`
	ReachableAt time.Time `json:"reachable_at,omitempty"`
	Misses      int       `json:"misses,omitempty"`
	LatencyMS   int       `json:"latency_ms,omitempty"`
	// ConfirmedAt is when this proxy last proved the device's identity. STICKY:
	// it records that proof happened, not that it is happening now — an outage
	// must not demote the only observer able to report it.
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	// ConfirmedBy says what matched, for a screen: "sysObjectID → MikroTik".
	ConfirmedBy string `json:"confirmed_by,omitempty"`
}

// Confirmed reports whether this proxy has ever proven the device's identity.
func (p ProxyReach) Confirmed() bool { return p.ConfirmedAt != nil }

// Up reports whether this proxy currently sees the device. False when it has not
// concluded yet: "no opinion" is not "online".
func (p ProxyReach) Up() bool { return p.Reachable != nil && *p.Reachable }

// Down reports whether this proxy has CONCLUDED the device is unreachable.
// A proxy with no verdict yet is neither up nor down.
func (p ProxyReach) Down() bool { return p.Reachable != nil && !*p.Reachable }

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
	// operator messages: "collected by proxy-lab" reads; a UUID does not.
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
// it, and for which sites it may serve — see docs/en/01-Architecture/Proxy.md.
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

	// Upgrade (see docs/en/01-Architecture/Proxy versioning and updates.md).
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
// adoption (cf. docs/en/01-Architecture/Discovery.md).
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

// APIToken is one personal API credential AS ITS OWNER'S LIST SHOWS IT, and
// that is the whole point of the type: there is no field here that could carry
// the secret, so no handler, log line, or later refactor can leak it by
// forgetting a `json:"-"`. The clear value exists once, in the body of the
// response that created it (cf. ADR-0011 and migration 0045).
//
// Prefix is the token's visible head (`mct_` plus the first characters of the
// selector). It is NOT a secret and not enough to authenticate — it exists so
// an owner looking at three tokens can tell which one is on which machine.
type APIToken struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	// ReadOnly / SiteIDs / ExpiresAt: the scope, which can only REDUCE what the
	// owner may do. An empty SiteIDs means "no site restriction", not "no
	// site"; a nil ExpiresAt means "never expires".
	ReadOnly   bool       `json:"read_only"`
	SiteIDs    []string   `json:"site_ids"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// APITokenRecord is what AUTHENTICATION needs, and nothing a screen needs: the
// stored verifier hash, the owner it resolves to, and the view above.
//
// Separate from APIToken, and with NO json tags anywhere, so that the only type
// which ever touches the hash is a type no response can be built from. The
// nullable fields are pointers because the judgement is made on them directly
// (cf. auth.judgeAPIToken): "revoked" must not be confused with "revoked at the
// zero instant".
type APITokenRecord struct {
	Token        APIToken
	User         User
	VerifierHash []byte
}

// APITokenMark heads every meshcraft API token. It makes a leaked value
// recognisable on sight — in a log, a pasted snippet, a wrong form field — as
// "somebody exposed a token" rather than as noise.
const APITokenMark = "mct_"

// apiTokenPrefixHint: how much of the (public) selector a token's visible head
// carries. Enough to tell three tokens apart in a list.
const apiTokenPrefixHint = 8

// APITokenPrefix builds the visible head shown beside a token's name.
//
// It lives in `model` rather than in `auth` because BOTH sides need it and the
// import arrow only goes one way (auth imports store imports model). One
// implementation, so a list and the thing that mints it cannot disagree about
// what a token looks like.
func APITokenPrefix(selector string) string {
	if len(selector) > apiTokenPrefixHint {
		selector = selector[:apiTokenPrefixHint]
	}
	return APITokenMark + selector
}

// --- Site extensions and the inventory they mirror (ADR-0015, ADR-0016) ---

// Extension kinds. A DATA value and not a Go `switch`, so that the second
// incarnation costs a row and a client, not a refactor of every call site.
const (
	ExtUniFiOS = "unifi_os"
)

// Extension transports. `direct` is the controller's own egress; `proxy` goes
// out through the proxy already posted on a site, which is what spared us a
// container for an HTTP client (ADR-0015 §3).
const (
	ExtTransportDirect = "direct"
	ExtTransportProxy  = "proxy"
)

// Mirror classes (ADR-0016). `access_point` is the one that justified the
// whole decision: it has radios, bands, channels and associated clients, and
// an access port has none of those.
const (
	ExtClassAccessPoint = "access_point"
	ExtClassSwitch      = "switch"
	ExtClassGateway     = "gateway"
	ExtClassOther       = "other"
)

// SiteExtension is one foreign controller backing a meshcraft site, AS A
// SCREEN SEES IT — so there is no field here that could carry the token. Like
// APIToken above, the absence is the safety: no handler or later refactor can
// leak the secret by forgetting a `json:"-"`. Presence is `HasToken`, and the
// clear value is reachable only through the traced reveal route.
type SiteExtension struct {
	ID     string `json:"id"`
	SiteID string `json:"site_id"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`

	BaseURL   string `json:"base_url"`
	HasToken  bool   `json:"has_token"`
	VerifyTLS bool   `json:"verify_tls"`
	Transport string `json:"transport"`
	ProxyID   string `json:"proxy_id,omitempty"`
	Enabled   bool   `json:"enabled"`

	// The pairing. RemoteSiteID is the key; RemoteSiteName is a cached label
	// that will be renamed and must never be matched on (ADR-0015 §6).
	RemoteSiteID   string     `json:"remote_site_id"`
	RemoteSiteRef  string     `json:"remote_site_ref"`
	RemoteSiteName string     `json:"remote_site_name"`
	PairedAt       *time.Time `json:"paired_at"`
	// OrphanedAt is set only when the foreign controller ANSWERED and the
	// paired id was absent from its list. A pointer because "orphaned" must
	// not be confused with "orphaned at the zero instant" — and because an
	// unreachable controller is a different, non-orphaning event.
	OrphanedAt *time.Time `json:"orphaned_at"`

	LastSyncAt *time.Time `json:"last_sync_at"`
	LastError  string     `json:"last_error"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ExtRadio is one 802.11 radio of an imported access point. The field names are
// the standard's, not UniFi's, because this shape is what a second Wi-Fi vendor
// will have to fill (ADR-0016). A radio is a SNAPSHOT: nothing references it,
// which is why it is a JSON child and not a table.
type ExtRadio struct {
	// "802.11n", "802.11ac", "802.11ax"… verbatim from the vendor.
	Standard string `json:"standard,omitempty"`
	// Band in GHz (2.4, 5, 6) — the band, not a port speed.
	BandGHz  float64 `json:"band_ghz,omitempty"`
	Channel  int     `json:"channel,omitempty"`
	WidthMHz int     `json:"width_mhz,omitempty"`
	// TxRetriesPct is radio quality, deliberately NOT fed through the traffic
	// ramp of ADR-0003: a radio capacity is not a port capacity and the same
	// ramp would state something false.
	TxRetriesPct *float64 `json:"tx_retries_pct,omitempty"`
}

// ExtPort is one copper port of an imported box — two, on an AC Pro. Enough to
// show an uplink speed; emphatically not a faceplate.
type ExtPort struct {
	Index        int    `json:"index"`
	State        string `json:"state,omitempty"`
	Connector    string `json:"connector,omitempty"`
	SpeedMbps    int    `json:"speed_mbps,omitempty"`
	MaxSpeedMbps int    `json:"max_speed_mbps,omitempty"`
}

// ExtDevice is one row of the read-only mirror. It is NOT a Device and never
// becomes one: `devices` carries the invariant "something meshcraft configures,
// with a connector", and an imported box already has a master (ADR-0015 §5).
type ExtDevice struct {
	ID          string `json:"id"`
	ExtensionID string `json:"extension_id"`
	SiteID      string `json:"site_id"`
	ExternalID  string `json:"external_id"`
	Class       string `json:"class"`
	Name        string `json:"name"`
	Model       string `json:"model"`
	Firmware    string `json:"firmware"`
	// The foreign controller's own word, lowercased. NOT mapped onto
	// Device.Status (ok|warn|off|unknown): mapping it would invent a judgement
	// meshcraft has no basis to make on a box it does not poll itself.
	State  string     `json:"state"`
	MgmtIP string     `json:"mgmt_ip"`
	MAC    string     `json:"mac"`
	Radios []ExtRadio `json:"radios"`
	Ports  []ExtPort  `json:"ports"`
	SeenAt time.Time  `json:"seen_at"`
}

// ExtSSID is a service set of the SITE, not of an access point: UniFi
// configures them at site level and every access point broadcasts them. One row
// per access point would create N copies to reconcile, and the first divergence
// would be indistinguishable from a real configuration drift (ADR-0016).
//
// No passphrase field exists, and that is deliberate: reading a controller does
// not mean copying the PSKs of a production network into a second database.
type ExtSSID struct {
	ID          string    `json:"id"`
	ExtensionID string    `json:"extension_id"`
	SiteID      string    `json:"site_id"`
	ExternalID  string    `json:"external_id"`
	Name        string    `json:"name"`
	Enabled     bool      `json:"enabled"`
	Security    string    `json:"security"`
	WPAMode     string    `json:"wpa_mode"`
	Bands       []string  `json:"bands"`
	SeenAt      time.Time `json:"seen_at"`
}

// ExtRemoteSite is a site OFFERED by the foreign controller, for the pairing
// screen to choose from. Transient: it is never stored, because storing the
// catalogue would be storing a name as if it were a key.
type ExtRemoteSite struct {
	ID   string `json:"id"`
	Ref  string `json:"ref"`
	Name string `json:"name"`
	// Paired tells the screen this remote site is already taken by an
	// extension, so the operator does not back two meshcraft sites with one
	// UniFi site without noticing.
	Paired bool `json:"paired"`
}

// ExtSyncReport is what one import pass did. Counted per class because "12
// imported" is useless when the question is whether the switches arrived.
type ExtSyncReport struct {
	RemoteSiteName string `json:"remote_site_name"`
	AccessPoints   int    `json:"access_points"`
	Switches       int    `json:"switches"`
	Others         int    `json:"others"`
	SSIDs          int    `json:"ssids"`
	// Removed counts mirror rows the foreign controller no longer lists. They
	// are deleted, unlike an orphaned PAIRING: a box absent from a site that
	// still answers has genuinely left that site.
	Removed  int       `json:"removed"`
	SyncedAt time.Time `json:"synced_at"`
}

// --- Alerting (ADR-0024) ---

// Alert classes. A channel subscribes to a subset, and a port event that falls
// in NO class produces nothing — that exclusion is the whole point of ADR-0024.
const (
	// AlertClassDevice: the equipment itself went unreachable or came back.
	AlertClassDevice = "device"
	// AlertClassInterco: a port facing another device of the fleet. Derived,
	// never stored — a recognized LLDP peer, or a declared device_links row.
	AlertClassInterco = "interco"
	// AlertClassDescribed: a port carrying an operator description
	// (devices.ports.<port>.label). "Described" means described IN MESHCRAFT,
	// by someone who opened the Ports screen to do it — not ifAlias, which no
	// connector reads (ADR-0024, Context).
	AlertClassDescribed = "described"
)

// Delivery states. `held` is the only non-terminal one.
const (
	// NotifyHeld: retained, not yet delivered. ReleaseAt says when.
	NotifyHeld = "held"
	// NotifySent: accepted by the channel.
	NotifySent = "sent"
	// NotifyFailed: attempts exhausted. THE ALERT EXISTED — this row is the
	// product's ability to say so when Telegram was unreachable.
	NotifyFailed = "failed"
	// NotifyCoalesced: absorbed into another delivery of the same key. This is
	// what a flapping port produces instead of six messages.
	NotifyCoalesced = "coalesced"
	// NotifySuppressed: dropped on purpose — attributable to a meshcraft
	// gesture, most often. SuppressedReason says which.
	NotifySuppressed = "suppressed"
)

// Channel kinds.
const (
	NotifyKindTelegram = "telegram"
	NotifyKindSlack    = "slack"
)

// NotifyChannel is one notification destination AS A SCREEN SEES IT — so there
// is no field here that could carry the token, exactly like SiteExtension and
// APIToken above. The absence is the safety: no handler or later refactor can
// leak the secret by forgetting a `json:"-"`. Presence is HasToken.
type NotifyChannel struct {
	ID string `json:"id"`
	// SiteID empty = instance channel: it receives events from every site.
	SiteID  string `json:"site_id,omitempty"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
	// Target is the non-secret addressing: a Telegram chat id. Empty for Slack,
	// whose webhook URL is addressing and credential at once and therefore
	// lives entirely in the vault.
	Target   string `json:"target"`
	HasToken bool   `json:"has_token"`

	Classes     []string `json:"classes"`
	MinSeverity int      `json:"min_severity"`

	// Channel health — the answer to "silence means what?" without sending a
	// heartbeat nobody reads (ADR-0024, Rejected alternatives).
	LastOKAt            *time.Time `json:"last_ok_at"`
	LastError           string     `json:"last_error"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	// DroppedUninteresting counts port events refused for want of a description
	// and of a known neighbour. ADR-0024's blind spot, turned into a number
	// somebody can read.
	DroppedUninteresting int64 `json:"dropped_uninteresting"`

	// Pending / Failed are computed on read from notify_deliveries, never
	// stored: they are what makes a mute channel readable as healthy or broken.
	Pending int `json:"pending"`
	Failed  int `json:"failed"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NotifyDelivery is the delivery slip of ONE journal line to ONE channel. It is
// not an alert record: the alert IS the events row it points at (ADR-0024).
type NotifyDelivery struct {
	ID        int64  `json:"id"`
	EventID   int64  `json:"event_id"`
	ChannelID string `json:"channel_id"`
	State     string `json:"state"`
	// AlertKey is (class, device, port), frozen at write time. Unlike
	// "interco", which is derived on read, this one MUST be stored: grouping
	// compares slips written at different instants, and a port described
	// mid-episode would otherwise change key halfway through.
	AlertKey    string     `json:"alert_key"`
	ReleaseAt   time.Time  `json:"release_at"`
	FirstAt     time.Time  `json:"first_at"`
	Transitions int        `json:"transitions"`
	Attempts    int        `json:"attempts"`
	NextAttempt *time.Time `json:"next_attempt_at"`
	LastError   string     `json:"last_error"`
	// SuppressedReason names the gesture an alert was attributed to, so a
	// silence is explainable after the fact.
	SuppressedReason string     `json:"suppressed_reason"`
	CreatedAt        time.Time  `json:"created_at"`
	SentAt           *time.Time `json:"sent_at"`
}

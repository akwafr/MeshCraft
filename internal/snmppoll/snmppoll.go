// Package snmppoll queries a device via SNMP and returns samples.
//
// Run by the PROXY, never the controller: a walk crossing the WAN in UDP costs one round
// trip per OID and fails on the first link incident (docs/fr/01-Architecture/Monitoring.md
// section 2).
//
// Only two OID sets, deliberately. `if_state` is what must stay fresh -- it says a port
// fell -- and is read at the fastest cadence the connector allows. `if_counters` is bulky
// and only serves rate calculation: reading it as often would cost much for little.
//
// This package also holds project CONNECTION CONVENTIONS, and holds them alone: Dial is the
// door everyone uses to open an SNMP session, including whoever walks an OID the poller
// does not know (Session.BulkWalk). Two places to write a timeout or community fallback is
// the certainty that one day diagnostic and supervision will describe the same device
// differently -- with nobody to signal it.
package snmppoll

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"meshcraft/internal/metricbuf"
)

// OIDs of tables read. Standard MIBs: any SNMP-speaking device exposes them, avoiding a
// per-vendor catalog for the essentials.
const (
	oidIfDescr       = "1.3.6.1.2.1.2.2.1.2"     // ifDescr
	oidIfType        = "1.3.6.1.2.1.2.2.1.3"     // ifType
	oidIfOperStatus  = "1.3.6.1.2.1.2.2.1.8"     // ifOperStatus
	oidIfHighSpeed   = "1.3.6.1.2.1.31.1.1.1.15" // ifHighSpeed (Mbit/s)
	oidIfHCInOctets  = "1.3.6.1.2.1.31.1.1.1.6"  // ifHCInOctets
	oidIfHCOutOctets = "1.3.6.1.2.1.31.1.1.1.10" // ifHCOutOctets
	oidIfInErrors    = "1.3.6.1.2.1.2.2.1.14"    // ifInErrors
	oidIfOutErrors   = "1.3.6.1.2.1.2.2.1.20"    // ifOutErrors
)

// Target is a device to query.
type Target struct {
	DeviceID  string `json:"device_id"`
	IP        string `json:"ip"`
	Community string `json:"community,omitempty"`
	// Version: "2c" by default. v3 will come with service identity.
	Version string `json:"version,omitempty"`
	Port    int    `json:"port,omitempty"`
	// MaxIfIndex bounds retained interfaces to physical front-panel ports.
	// 0 = no bound. See tunnel.SNMPWatch.
	MaxIfIndex int `json:"max_ifindex,omitempty"`
}

// Metric names an OID set.
const (
	MetricIfState    = "if_state"
	MetricIfCounters = "if_counters"
)

// IfTypeEthernet is `ethernetCsmacd(6)` from IANAifType-MIB: a physical Ethernet port.
//
// The ONLY vendor-neutral answer to "is this a front-panel port?", and one was needed.
// Survey on a 1930: VLAN interfaces use the VLAN NUMBER as label -- VLAN 42 interface is
// named "42", exactly like physical port 42. Trusting the label would write VLAN traffic
// on a port, i.e. a credible false graph. Trusting an index bound worked by luck on that
// line alone.
const IfTypeEthernet = 6

// Client opens an SNMP session. Isolated behind an interface so OID-to-sample translation,
// which is what goes wrong, is testable without device or network.
type Client interface {
	// BulkWalk walks a subtree and returns, per interface index, the raw value.
	BulkWalk(ctx context.Context, oid string) (map[string]gosnmp.SnmpPDU, error)
	Close() error
}

// Poller queries a target and fills a buffer.
type Poller struct {
	Target Target
	// Dial is injectable for tests. Nil = real SNMP.
	Dial func(Target) (Client, error)
	// DialTable opens the FULL-OID-indexed view needed when table index spans multiple arcs
	// (cf. TableClient and PollLLDP). Nil = real SNMP.
	//
	// Second field, not conversion of the first: the two views lose different things on
	// purpose, and a neighbor table read through `Client` would overwrite neighbors on all
	// ports onto each other.
	DialTable func(Target) (TableClient, error)
}

// suffixIndex returns interface index from a table OID: last segment.
func suffixIndex(oid string) string {
	var i = strings.LastIndex(oid, ".")
	if i < 0 {
		return oid
	}
	return oid[i+1:]
}

// toFloat converts an SNMP value to a number and reports whether it is usable.
// String or unexpected type is NOT converted to zero: zero counter reads as inactive link,
// which would be a quiet lie.
//
// toFloat and asString serve ONLY metric production, not diagnostic conversion
// (cmd/proxy/rpc.go, pduValueText). Gap is INTENTIONAL and signaled both sides: here we
// want float64 and REFUSE what is not one; there we want exact text -- Counter64 beyond 2^53
// does not survive float, non-UTF-8 bytes do not survive JSON. Unifying would lose fidelity
// on diagnostic or simplicity on metric.
func toFloat(pdu gosnmp.SnmpPDU) (float64, bool) {
	switch pdu.Type {
	case gosnmp.Integer, gosnmp.Counter32, gosnmp.Counter64, gosnmp.Gauge32,
		gosnmp.TimeTicks, gosnmp.Uinteger32:
		// Only short declaration in the package, and irreducible: a type switch with binding
		// has no `var` form in Go. Project rule (CLAUDE.md) does not apply; not a cleanup
		// oversight. Rewriting without binding would repeat type assertion in five branches
		// for identical bodies.
		switch v := pdu.Value.(type) {
		case int:
			return float64(v), true
		case int64:
			return float64(v), true
		case uint:
			return float64(v), true
		case uint64:
			return float64(v), true
		case uint32:
			return float64(v), true
		}
	}
	return 0, false
}

func asString(pdu gosnmp.SnmpPDU) string {
	var b, okB = pdu.Value.([]byte)
	if okB {
		return string(b)
	}
	var s, okS = pdu.Value.(string)
	if okS {
		return s
	}
	return ""
}

// Samples translates read tables into samples.
//
// PURE function, and that is where correctness lives: associating a counter with the wrong
// port produces a credible false graph, worse than a gap. Hence strict index-by-ifIndex, and
// port name carried as label not identifier.
func Samples(deviceID string, tables map[string]map[string]gosnmp.SnmpPDU,
	at time.Time, maxIfIndex int) []metricbuf.Sample {
	var out []metricbuf.Sample
	var names = map[string]string{}
	var idx string
	var pdu gosnmp.SnmpPDU
	for idx, pdu = range tables[oidIfDescr] {
		names[idx] = asString(pdu)
	}
	var n float64
	var isNum bool

	var emit = func(oid, metric string) {
		for idx, pdu = range tables[oid] {
			if !physical(idx, tables[oidIfType], maxIfIndex) {
				continue
			}
			var v, ok = toFloat(pdu)
			if !ok {
				continue
			}
			var labels = map[string]string{"device": deviceID, "ifindex": idx}
			if names[idx] != "" {
				labels["port"] = names[idx]
			}
			// `iftype` travels with sample to controller, which refuses inventory conclusions
			// without it. Not redundancy: proxy is separately deployed binary, controller must
			// not depend on proxy version.
			var t, hasType = tables[oidIfType][idx]
			if hasType {
				n, isNum = toFloat(t)
				if isNum {
					labels["iftype"] = strconv.Itoa(int(n))
				}
			}
			out = append(out, metricbuf.Sample{
				Name: metric, Labels: labels, Value: v, TS: at,
			})
		}
	}
	// Series names come from `metricbuf` and are no longer written here: this package is not
	// the only sample producer (MikroTik connectors emit same series from other channels),
	// and two producers of one series is exactly where a copied name diverges by one letter
	// -- curve breaks when device changes line with no screen signal.
	emit(oidIfOperStatus, metricbuf.MetricIfOperStatus)
	emit(oidIfHighSpeed, metricbuf.MetricIfSpeedMbps)
	emit(oidIfHCInOctets, metricbuf.MetricIfInOctets)
	emit(oidIfHCOutOctets, metricbuf.MetricIfOutOctets)
	emit(oidIfInErrors, metricbuf.MetricIfInErrors)
	emit(oidIfOutErrors, metricbuf.MetricIfOutErrors)
	return out
}

// physical rejects what is not a front-panel port.
//
// `ifType` rules when device provides it, and it almost always does: mandatory MIB-II
// column. Only vendor-neutral answer, and it clears three parasite families seen on a 1930
// -- `TRK*` aggregates (index 1000+), tunnels (3000+), VLAN interfaces (100000+).
//
// Neither index nor label sufficed. Label collides: VLAN 42 interface is "42", like physical
// port 42. Index bound worked because that line numbers ports 1..52 -- vendor coincidence,
// not protocol property, and fails on first device that numbers differently.
//
// Index bound remains FALLBACK for rare case where `ifType` table could not be read. Old
// heuristic beats no metric at all.
func physical(idx string, ifTypes map[string]gosnmp.SnmpPDU, maxIfIndex int) bool {
	var pdu, seen = ifTypes[idx]
	if seen {
		var t, ok = toFloat(pdu)
		return ok && int(t) == IfTypeEthernet
	}
	if len(ifTypes) > 0 {
		// Table was read and this index is missing: not an interface the device declares. Do
		// not invent it.
		return false
	}
	if maxIfIndex <= 0 {
		return true
	}
	var n, err = strconv.Atoi(idx)
	return err == nil && n >= 1 && n <= maxIfIndex
}

// oidsFor returns tables to walk for a metric set.
func oidsFor(metric string) []string {
	switch metric {
	case MetricIfCounters:
		return []string{oidIfDescr, oidIfType, oidIfHCInOctets, oidIfHCOutOctets,
			oidIfInErrors, oidIfOutErrors}
	case MetricHealth:
		// Device sensors: uptime, CPU, memory, temperatures, voltages, fans, PSUs. See
		// health.go, whose header costs this set and states what its scale factors are worth.
		return healthOIDs()
	case MetricOptical:
		// SFP cage optical diagnostics, MikroTik proprietary table.
		return opticalOIDs()
	default:
		// `if_state` stays deliberately narrow: most-read set, every OID here is paid every
		// cycle on every device.
		//
		// `ifType` is included anyway, assumed trade: one more table per cycle against
		// certainty of not attributing VLAN traffic to a physical port. Same size as `ifDescr`
		// already read here, walk bounded by interface count -- not OID count.
		return []string{oidIfDescr, oidIfType, oidIfOperStatus, oidIfHighSpeed}
	}
}

// Poll queries the target for a metric set.
func (p *Poller) Poll(ctx context.Context, metric string) ([]metricbuf.Sample, error) {
	var dial = p.Dial
	if dial == nil {
		dial = dialSNMP
	}
	var c, err = dial(p.Target)
	if err != nil {
		return nil, err
	}
	defer c.Close() //nolint:errcheck

	var tables = map[string]map[string]gosnmp.SnmpPDU{}
	var oid string
	for _, oid = range oidsFor(metric) {
		var t map[string]gosnmp.SnmpPDU
		if t, err = c.BulkWalk(ctx, oid); err != nil {
			// Missing table is not outage: not all devices expose ifXTable. Return what we read.
			continue
		}
		tables[oid] = t
	}
	if len(tables) == 0 {
		return nil, errNoTable
	}
	// TRANSLATION depends on set, not only OIDs. Two sensor sets differ in indexing and
	// labels: health describes DEVICE (no port label), optical describes a CAGE (ifIndex and
	// port name), while state and counters describe whole facade. Forcing them through
	// `Samples` would label temperature series with a random port.
	var at = time.Now()
	switch metric {
	case MetricHealth:
		return HealthSamples(p.Target.DeviceID, tables, at), nil
	case MetricOptical:
		return OpticalSamples(p.Target.DeviceID, tables, at, p.Target.MaxIfIndex), nil
	}
	return Samples(p.Target.DeviceID, tables, at, p.Target.MaxIfIndex), nil
}

type snmpError string

func (e snmpError) Error() string { return string(e) }

const errNoTable = snmpError("no readable SNMP table on this device")

// DefaultCommunity: community used when target gives none.
//
// Empty means "public", not "no SNMP": historical fleet fallback. Exported because same
// rule must hold EVERYWHERE a community is used or compared -- device polled as "public"
// but traps refused because another proxy path reads empty field differently is a bug hunted
// on the wrong side.
const DefaultCommunity = "public"

// defaultPort: standard SNMP port. Not exported -- nobody needs to read it, only not rewrite
// it elsewhere.
const defaultPort = 161

// Session is an SNMP session opened TO PROJECT CONVENTIONS.
//
// Exported for a precise reason: these conventions must be written ONCE. They were written
// twice (here and proxy ask responder, cf. cmd/proxy/rpc.go), and danger was not the copy
// -- it was invisibility. Timeout or community fallback changed on one side only, and
// diagnostic walk describes a different device than ordinary poll, with no signal: we blame
// device outage where client diverged.
type Session struct{ g *gosnmp.GoSNMP }

// Dial opens a session to t. ONLY entry point: caller needing walk outside poller's two OID
// sets goes here rather than copying parameters.
func Dial(t Target) (*Session, error) {
	var g = conventions(t)
	var err = g.Connect()
	if err != nil {
		return nil, err
	}
	return &Session{g: g}, nil
}

// conventions applies project connection conventions WITHOUT opening a socket.
//
// Separate from Dial so tests can read them as-is, without network or device: that test
// fails if any of five values moves (cf. TestConventionsDeConnexionUniques). Conventions
// nothing verifies drift on their own.
func conventions(t Target) *gosnmp.GoSNMP {
	var port = t.Port
	if port <= 0 {
		port = defaultPort
	}
	var community = t.Community
	if community == "" {
		community = DefaultCommunity
	}
	return &gosnmp.GoSNMP{
		Target:    t.IP,
		Port:      uint16(port), //nolint:gosec
		Community: community,
		// v2c for everyone, even when Target.Version asks otherwise: v3 comes with service
		// identity, and trying v3 without identity would fail cold on whole fleet.
		Version: gosnmp.Version2c,
		// Tight bounds: mute device must not hold a collection cycle. Two tries, no more --
		// metric will return. Same bounds for diagnostic walk: looser, it would succeed where
		// supervision fails and we believe supervision "loses" OIDs.
		Timeout: 2 * time.Second,
		Retries: 1,
		MaxOids: 60,
	}
}

// BulkWalk walks an ARBITRARY subtree and passes each PDU to fn as-is, in walk order.
//
// gosnmp signature deliberately: raw form -- full OID name, ASN.1 type, unconverted value
// -- needed by whoever must RETURN what was read rather than derive a number. No context
// because gosnmp takes none: caller watches cancel from fn, only resume point between PDUs.
func (s *Session) BulkWalk(rootOID string, fn gosnmp.WalkFunc) error {
	return s.g.BulkWalk(rootOID, fn)
}

func (s *Session) Close() error { return s.g.Conn.Close() }

// pollClient adapts Session to Client contract: table INDEXED by interface index that Samples
// needs. Poller view, not diagnostic -- loses walk order, keeps one PDU per index, exactly
// what metrics need and exactly what must not happen when returning a table to an operator.
type pollClient struct{ s *Session }

func dialSNMP(t Target) (Client, error) {
	var s, err = Dial(t)
	if err != nil {
		return nil, err
	}
	return &pollClient{s: s}, nil
}

func (c *pollClient) BulkWalk(ctx context.Context, oid string) (map[string]gosnmp.SnmpPDU, error) {
	var out = map[string]gosnmp.SnmpPDU{}
	var err = c.s.BulkWalk(oid, func(pdu gosnmp.SnmpPDU) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		out[suffixIndex(pdu.Name)] = pdu
		return nil
	})
	return out, err
}

func (c *pollClient) Close() error { return c.s.Close() }

// ParseInterval reads cadence in seconds, respecting connector FLOOR.
//
// Floor is not advisory: going below degrades device control plane before metric. Faster
// request is clamped to floor -- refusing would drop supervision for a typo.
func ParseInterval(wanted, floor time.Duration) time.Duration {
	if floor <= 0 {
		floor = 30 * time.Second
	}
	if wanted < floor {
		return floor
	}
	return wanted
}

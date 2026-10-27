// Package gnmipoll subscribes to a device's gNMI telemetry and returns samples.
//
// Meant to run on the PROXY, beside internal/snmppoll and for the same reason: a
// subscription is a long-lived connection to a device on the management network,
// and holding it from the controller would put every sample of every fleet
// across the WAN on one process's connection budget.
//
// # THE TRANSPORT IS BUILT, AND THIS PACKAGE HAS A PRODUCTION CALLER
//
// cmd/proxy/gnmi.go is it: it turns the watches the controller hands the proxy
// into one [Collector] per device and metric, and feeds their samples into the
// same internal/metricbuf the SNMP poller fills. [Transport] speaks real gRPC
// (transport.go, via github.com/openconfig/gnmic/pkg/api) and [TransportReady]
// is true. The subscription has been exercised end to end against a lab Arista
// (internal/driver/arista/eos, EOS 4.28.13.1M).
//
// NONE OF THAT WAS TRUE WHEN THIS PACKAGE WAS WRITTEN, and the order it was
// built in is the part worth keeping. Everything that does not need a wire — the
// OpenConfig path vocabulary, the path parser, and the pure translation from a
// notification into [metricbuf.Sample] — was written and tested first, against
// handcrafted notifications, while [Transport] was an interface with one honest
// implementation that refused. Those are the parts where a mistake is SILENT: a
// counter attributed to the wrong port produces a credible false graph, which is
// worse than no graph. The wire landed afterwards with the translation already
// proven, which is why its own tests are about the wire and not about arithmetic.
//
// # THE gRPC DEPENDENCY, which was a decision and not a detail
//
// gNMI is gRPC over HTTP/2 carrying protobuf. Speaking it properly means
// carrying google.golang.org/grpc, google.golang.org/protobuf and
// github.com/openconfig/gnmi, and TODO.md posed that against hand-rolling the
// framing and the decoding over the golang.org/x/net/http2 this module already
// had. The real client is what was chosen; transport.go's header records why —
// a hand-written decoder that is wrong does not fail, it produces exactly the
// credible false graph this package is built to avoid — and what it costs, which
// is the largest single thing meshcraft now has to keep patched.
//
// # Authentication, assumed and stated
//
// EOS authenticates a gNMI subscriber with an ordinary AAA account: username and
// password travel as gRPC metadata, in clear when the transport carries no TLS
// (`transport grpc default` with no ssl profile — cf.
// internal/driver/arista/eos/gnmi.go). [Target] therefore carries a DEVICE
// LOGIN, not a read-only community, and that difference is the reason this
// package's Target is not simply snmppoll's with a port changed: a leaked v2c
// community reads a MIB, a leaked device login configures the switch.
//
// Unverified, and named as such: whether a `network-operator` account suffices
// for a Subscribe, or whether the subscription needs the network-admin account
// meshcraft provisions.
package gnmipoll

import (
	"context"
	"time"

	"meshcraft/internal/metricbuf"
)

// Metric names a SUBSCRIPTION SET, and reuses snmppoll's vocabulary on purpose.
//
// The controller resolves cadences per metric name
// (driver.MonitoringCapabilities.BestOfKind(kind, metric)), and those names are
// the contract between a connector's declaration and whatever collects it. A
// gNMI collector inventing its own spelling would make a connector unable to
// declare, in one vocabulary, what it can serve over two protocols.
const (
	MetricIfState    = "if_state"
	MetricIfCounters = "if_counters"
	// MetricHealth: uptime, CPU, memory and the environmental sensors, read from
	// the OpenConfig `/system` and `/components` trees rather than from a vendor
	// enterprise MIB. That is the difference worth naming: snmppoll's `health`
	// set is MIKROTIK-MIB and Cisco-specific branches, one implementation per
	// vendor, whereas these paths are modelled and the same on every target that
	// serves them.
	MetricHealth = "health"
	// MetricLLDP: the neighbour table. NOT a sample set — it produces a
	// [snmppoll.LLDPReport], the same snapshot shape the SNMP poller pushes, and
	// it travels by its own HTTP route. See lldp.go for why a table cannot ride
	// the metric buffer.
	MetricLLDP = "lldp"
	// MetricOptical: transceiver DDM readings — receive/transmit power and laser
	// bias, per physical channel. The same name the SNMP poller uses
	// (snmppoll.MetricOptical) for the same reason the constants above reuse its
	// vocabulary: the connector declaration and the collector must speak one
	// language whatever the protocol underneath.
	MetricOptical = "optical_power"
)

// DefaultPort is the port EOS serves gNMI on. Duplicated from the connector's
// [eos.DefaultGNMIPort] rather than imported: the proxy must not import a
// connector — it is a separately deployed binary that carries no driver
// knowledge, and this is a wire default, not a platform capability.
const DefaultPort = 6030

// Target is a device to subscribe to.
type Target struct {
	DeviceID string `json:"device_id"`
	IP       string `json:"ip"`
	// Port: 0 means [DefaultPort].
	Port int `json:"port,omitempty"`
	// Username / Password: a DEVICE LOGIN (see the package header). Not a
	// community, not a token, and not something to default silently — a target
	// arriving without them is a target that cannot be subscribed to, and
	// subscribing anyway would produce an authentication failure per retry, for
	// ever, against a switch's management CPU.
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// Plaintext: subscribe over an UNENCRYPTED gRPC connection.
	//
	// OPT-IN, and false is the default on purpose — this is the one field in the
	// struct whose wrong value is silent and expensive. [Target] carries a device
	// login (see above); over plaintext that login travels as gRPC metadata in
	// clear on the management network, so anyone with a port mirror gets a
	// privileged switch account. `transport grpc default` with no ssl profile —
	// what internal/driver/arista/eos/gnmi.go writes when no profile is asked
	// for — is exactly that case.
	//
	// Defaulting it to true would have been the convenient choice: it is what
	// gnmic's `--insecure` does and what makes a lab switch stream on the first
	// try. It is refused here because the failure mode is inverted — a target
	// that should have been encrypted and was not produces WORKING TELEMETRY,
	// with nothing on screen to say the password went out in the clear. A target
	// that should have been plaintext and was not simply fails to connect, loudly,
	// on the first attempt.
	Plaintext bool `json:"plaintext,omitempty"`
	// SkipVerify: accept the target's certificate without checking it.
	//
	// Meaningful only when [Plaintext] is false. It exists because these switches
	// ship self-signed certificates and a site that has not yet run the
	// certificate work has no CA to check against — refusing outright would leave
	// TLS unusable and push every operator to Plaintext, which is strictly worse:
	// an unverified TLS session still encrypts the login against a passive
	// listener, and only plaintext does not.
	//
	// It is NOT equivalent to verification: an active attacker on the management
	// path can present their own certificate. Supply [CAPEM] once there is a CA.
	SkipVerify bool `json:"skip_verify,omitempty"`
	// CAPEM: the PEM-encoded authority the target's certificate must chain to.
	//
	// Set, it makes [SkipVerify] unnecessary and is the only configuration in
	// this struct that is actually safe against someone on the path. Empty means
	// the system pool, which on a proxy image holds no switch's issuer — hence
	// SkipVerify existing at all.
	CAPEM string `json:"ca_pem,omitempty"`
}

// Subscription is one path the device is asked to stream, and how often it
// should SAMPLE it.
//
// The interval belongs to the subscription and not to a poll loop, which is the
// whole difference between this and snmppoll: with STREAM/SAMPLE the device
// decides when to send, and the collector's only cadence control is what it
// asked for at subscribe time. A collector that "polled faster" here would
// simply be re-subscribing.
type Subscription struct {
	Path Path
	// Mode is the STREAM sub-mode, spelled out rather than inferred.
	//
	// It used to be inferred — interval 0 was read as "on change" — and that was
	// wrong in the one place it mattered: in gNMI, a subscription with no mode is
	// TARGET_DEFINED, and a target is free to answer it with a periodic sample.
	// An `if_state` subscription that meant ON_CHANGE and was sent as
	// TARGET_DEFINED still produces a graph; it just stops being a stream, and
	// the port-down event it exists to carry arrives at whatever cadence the
	// switch felt like. Nothing anywhere would have said so.
	Mode SubMode
	// SampleInterval is how often the DEVICE samples the path. Meaningful for
	// [SubSample] only; ignored — and not sent — for [SubOnChange].
	//
	// A counter must never be ON_CHANGE: a busy port reports continuously and an
	// idle one never reports at all, so the series has no cadence and its
	// derivative is meaningless.
	SampleInterval time.Duration
}

// SubMode is the gNMI STREAM sub-mode.
//
// Three values, and the zero one is TARGET_DEFINED because that is what the
// protocol's zero means too — a Go zero value that quietly meant something the
// wire does not is how the bug above happened. A caller that wants a guarantee
// names it.
type SubMode int

const (
	// SubTargetDefined: the device chooses. Never asked for deliberately in this
	// build; it is here because it is the protocol's default and the type must be
	// able to say so.
	SubTargetDefined SubMode = iota
	// SubOnChange: the device sends when the value changes, and only then.
	SubOnChange
	// SubSample: the device sends every [Subscription.SampleInterval].
	SubSample
)

// ValueKind says which of [Value]'s fields carries the payload.
//
// gNMI values are a protobuf oneof, and the two cases this product needs are not
// interchangeable: counters arrive as numbers, and oper-status and port-speed
// arrive as STRING enums. Collapsing them into a float64 with an "ok" flag — the
// shape snmppoll's toFloat uses — would force the enum decoding to happen at the
// wire boundary, which is exactly where it cannot be tested.
type ValueKind int

const (
	// ValueNone: no usable payload. Never translated into a sample, and in
	// particular never into a zero (cf. snmppoll.toFloat's rule: a zero counter
	// reads as an idle link, which is a quiet lie).
	ValueNone ValueKind = iota
	ValueNumber
	ValueString
	// ValueJSON: the update's payload is a JSON document describing an entire
	// SUBTREE, not one leaf.
	//
	// THE CASE THAT WAS MISSING, and its absence was silent. This package
	// subscribes containers (`…/state/counters`) because one container is one
	// notification instead of ten. Under PROTO encoding a container subscription
	// still arrives decomposed — one update per leaf, each with its own path —
	// which is the shape everything downstream is written for. Under JSON or
	// JSON_IETF it does not: the device answers with ONE update whose path stops
	// at the container and whose value is an object holding the whole subtree.
	//
	// With no case for it, `leafSuffix` on such an update yields "state/counters",
	// which is in no table, so every counter was dropped — silently, with a
	// healthy subscription and an empty graph. And since a SubscribeRequest that
	// names no encoding means JSON, that was the DEFAULT outcome, not an edge
	// case. [Expand] is what turns one of these back into per-leaf updates.
	ValueJSON
)

// Value is one leaf's payload, in neutral form.
type Value struct {
	Kind ValueKind
	Num  float64
	Str  string
	// JSON is the raw document, for [ValueJSON]. Kept as bytes rather than as a
	// decoded map so that the decoding — which can fail — happens in one tested
	// place instead of at every wire boundary.
	JSON []byte
}

// NumberValue / StringValue / JSONValue build the three carrying cases.
func NumberValue(f float64) Value { return Value{Kind: ValueNumber, Num: f} }
func StringValue(s string) Value  { return Value{Kind: ValueString, Str: s} }
func JSONValue(b []byte) Value    { return Value{Kind: ValueJSON, JSON: b} }

// Update is one leaf of one notification.
type Update struct {
	// Path is RELATIVE to the notification's prefix. Joining is the caller's job
	// and [Join] is where it happens — see the warning on [Notification.Prefix].
	Path  Path
	Value Value
}

// Notification is what a device sends, in neutral form.
//
// Deliberately NOT the protobuf type. When a transport exists, its only job is
// to map protobuf into this — and that job is small, mechanical, and reviewable,
// whereas the translation into samples below is neither. Splitting them here is
// what lets the interesting half be tested with no wire at all.
type Notification struct {
	// Prefix is factored out of every update in the notification, and THAT IS THE
	// CLASSIC WAY TO ATTRIBUTE A COUNTER TO THE WRONG PORT: a device that sends
	// prefix `/interfaces/interface[name=Ethernet49]` and update `state/counters/
	// in-octets` is describing Ethernet49, and a reader that looked at the update
	// alone would find no port at all — or, worse, would find the port of the
	// PREVIOUS notification if it kept any state.
	Prefix Path
	// TS is the device's own timestamp for the sample. Zero means the device gave
	// none, and the collector then stamps arrival time.
	TS     time.Time
	Update []Update
	// Delete: paths the device says NO LONGER EXIST, relative to [Prefix] like
	// the updates.
	//
	// THE HALF A POLL DOES NOT HAVE, and the reason it is on the type rather than
	// left out. A poll rediscovers the world every cycle, so something that went
	// away is simply absent from the next read. A subscription only sends
	// CHANGES, and a removal is a change with no value attached — so a collector
	// that ignored this field would keep a neighbour, and keep asserting it, long
	// after the cable was pulled.
	//
	// It is deliberately NOT consulted by [Samples]. A deleted counter is not a
	// counter worth a zero, and the metric pipeline's own staleness handling is
	// what deals with a series that stops arriving. It matters for the LLDP
	// table, which is a set and not a curve — see lldp.go.
	Delete []Path
}

// Stream is one live subscription: the notifications, and WHY it ended.
//
// # Why this is not just a channel
//
// It was one, and the reason it stopped being one is core01.lab on
// 2026-08-10. A gNMI subscription authenticates on the RPC, not on the dial, so
// a wrong password does not fail [Subscriber.Subscribe] — it fails once the
// stream is open, and the transport learned about it in a goroutine that had
// nowhere to put it. The error was logged locally at Debug and the channel was
// closed, which is indistinguishable from a device hanging up: [Collector.Run]
// returned nil, the supervisor above it read that as "the stream ended cleanly,
// the target is healthy", reset its backoff, and resubscribed for an hour. There
// was no fact anywhere for anything to report.
//
// So the reason travels with the stream. A channel alone cannot carry it: the
// failure happens after Subscribe has returned, and the transport is a stateless
// value shared by every subscription, so there is nowhere else to hang it.
type Stream struct {
	// C carries the notifications and is CLOSED when the subscription ends,
	// whatever ended it. The implementation owns it and must close it when ctx is
	// done.
	C <-chan Notification
	// Ended reports why the stream ended, and nil when it ended cleanly.
	//
	// Valid only once [Stream.C] is closed: before that the subscription has not
	// ended and there is no reason to give. Reading it after observing the close
	// is safe without any lock of the caller's — the close of a channel
	// happens-before the receive that observes it, which is the edge the
	// implementation writes behind.
	//
	// Nil is allowed and means "this implementation does not say", which is what
	// a test fake replaying a fixed list wants. Call it through [Stream.Reason].
	Ended func() error
}

// Reason is the nil-safe read of [Stream.Ended].
//
// It exists so that no caller has to write the two nil checks, because a caller
// that skips them does not crash — it silently loses the reason again, which is
// the exact defect this type was introduced to fix.
func (s *Stream) Reason() error {
	if s == nil || s.Ended == nil {
		return nil
	}
	return s.Ended()
}

// Subscriber opens a subscription and streams notifications.
//
// Isolated behind an interface for the same reason snmppoll.Client is: what goes
// wrong is the translation, not the socket, and translation must be testable
// with no device and no network. Here it does a second job — it is the seam the
// unbuilt transport sits behind, so every layer above can be finished and proven
// while the wire does not exist.
type Subscriber interface {
	// Subscribe opens the subscription. On a nil error it returns a non-nil
	// [Stream] whose channel it owns and closes when ctx is done.
	Subscribe(ctx context.Context, t Target, subs []Subscription) (*Stream, error)
}

type gnmiError string

func (e gnmiError) Error() string { return string(e) }

// DefaultSampleInterval is the cadence used when a caller resolves none.
//
// A FALLBACK and not a policy: the cadence is the controller's to decide, from
// the connector's declared floor, exactly as it is for SNMP
// (internal/api/watch_handlers.go). This constant only stops a caller that
// forgot from producing a SAMPLE subscription with no interval, which the target
// would answer at a rate of its own choosing.
const DefaultSampleInterval = 30 * time.Second

// SubscriptionsFor returns the paths a metric set needs, at the cadence the
// caller resolved.
//
// The analogue of snmppoll.oidsFor, and split the same way and for the same
// reason: `if_state` is what must stay fresh — it says a port fell — while
// `if_counters` is bulky and only serves rate computation. Asking a device to
// stream the second at the first's cadence costs the switch's CPU for nothing.
//
// # The interval is a PARAMETER, and it did not use to be
//
// It was hard-coded at 30 s here, which made every connector's declared
// MinInterval decorative: the controller resolved a cadence, wrote it into the
// watch, and the collector ignored it. Nothing failed — a stream arrived, graphs
// filled — and the only symptom was that changing the declared floor changed
// nothing at all. That is the shape of bug this package's header warns about, so
// the cadence now comes in from the caller and the fallback is named.
//
// `interval` is IGNORED for the on-change sets, and that is not an oversight: an
// ON_CHANGE subscription has no cadence to set, and a caller passing one is
// asking for something the protocol does not express.
func SubscriptionsFor(metric string, interval time.Duration) []Subscription {
	if interval <= 0 {
		interval = DefaultSampleInterval
	}
	switch metric {
	case MetricIfCounters:
		return []Subscription{
			{Path: pathCounters, Mode: SubSample, SampleInterval: interval},
		}
	case MetricHealth:
		return []Subscription{
			{Path: pathSystemState, Mode: SubSample, SampleInterval: interval},
			{Path: pathSystemMemory, Mode: SubSample, SampleInterval: interval},
			{Path: pathCPUState, Mode: SubSample, SampleInterval: interval},
			{Path: pathComponentState, Mode: SubSample, SampleInterval: interval},
			{Path: pathPSUState, Mode: SubSample, SampleInterval: interval},
		}
	case MetricOptical:
		// SAMPLE and not ON_CHANGE, although a quiet fibre's power barely moves:
		// EOS stamps a notification with the instant the value last changed (see
		// usableDeviceTS), so an on-change subscription on a stable link would go
		// silent for days and the series would read as dead collection. A sampled
		// stream re-publishes at the cadence asked, which is exactly what a level
		// HISTORY needs.
		return []Subscription{
			{Path: pathTransceiverChannel, Mode: SubSample, SampleInterval: interval},
		}
	case MetricLLDP:
		// ON CHANGE, and this is the one place streaming genuinely beats the SNMP
		// poller rather than merely replacing it: a neighbour table changes when a
		// cable moves, which is an event, and the LLDP-MIB walk it replaces cost a
		// full table read every thirty seconds to notice one.
		return []Subscription{
			{Path: pathLLDPNeighbor, Mode: SubOnChange},
			{Path: pathLLDPLocal, Mode: SubOnChange},
		}
	default:
		// if_state. ON CHANGE: the state of a port changes rarely and matters
		// immediately, which is precisely the case streaming exists for and the
		// one thing a poll can never do well.
		return []Subscription{
			{Path: pathIfState, Mode: SubOnChange},
			{Path: pathEthernetState, Mode: SubOnChange},
		}
	}
}

// Collector runs one subscription and feeds a sink.
//
// It holds NO buffer of its own, deliberately. The proxy already has exactly one
// (internal/metricbuf, with a Take/Ack cycle giving at-least-once delivery and
// one saturation counter); a second producer with a second buffer would give two
// drop counters, and a monitoring stack that drops samples in a place its own
// saturation report does not mention is a monitoring stack that lies. Hence the
// sink function: whoever wires this in hands it the existing buffer's AddAll.
type Collector struct {
	Target Target
	// Metric names the subscription set (see [SubscriptionsFor]).
	Metric string
	// Interval is the cadence the CONTROLLER resolved for this metric, bounded
	// by the connector's declared floor. Zero falls back to
	// [DefaultSampleInterval]; it is not a licence to poll fast.
	//
	// Ignored for the on-change sets, which have no cadence to set.
	Interval time.Duration
}

// Run subscribes and translates until the context ends or the stream ends.
//
// It returns the subscribe error, or — when the stream ended — [Stream.Reason],
// which is nil for an ordinary end and carries the gRPC status otherwise. It
// used to return a flat nil in that second case, so an authentication refusal
// and a device closing a healthy stream were the same value to every caller
// above; see [Stream] for what that cost.
//
// Notifications producing no sample are NOT reported to the sink at all — an
// empty batch would cost a lock on the proxy's buffer for nothing, and a device
// streaming a leaf this build does not understand would otherwise wake the whole
// pipeline on every update.
func (c *Collector) Run(ctx context.Context, sub Subscriber,
	sink func([]metricbuf.Sample)) error {
	var st, err = sub.Subscribe(ctx, c.Target, SubscriptionsFor(c.Metric, c.Interval))
	if err != nil {
		return err
	}
	if st == nil {
		// The interface says a nil error comes with a stream. An implementation
		// that breaks that contract must not take the collector down with a nil
		// dereference on the proxy's hot path.
		return gnmiError("gnmi subscribe returned neither a stream nor an error")
	}
	var n Notification
	var open bool
	for {
		select {
		case <-ctx.Done():
			// The subscriber owns its channel and closes it on ctx; we do not
			// drain it here. Waiting for the close would hang on an
			// implementation that is slow to notice cancellation, and this loop
			// is the one thing that must always be able to stop. No reason is
			// read either: we are the ones stopping, so there is nothing the
			// device could be blamed for.
			return nil
		case n, open = <-st.C:
			if !open {
				return st.Reason()
			}
			var out = Samples(c.Target.DeviceID, n, time.Now())
			if len(out) > 0 && sink != nil {
				sink(out)
			}
		}
	}
}

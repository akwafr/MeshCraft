package gnmipoll

// The wire. gNMI Subscribe over gRPC, spoken with the openconfig gnmic client.
//
// # THE DEPENDENCY DECISION THIS FILE EMBODIES
//
// TODO.md posed it as a choice between (a) the real client — grpc + protobuf +
// openconfig/gnmi — and (b) hand-rolling gRPC framing and protobuf decoding over
// the `golang.org/x/net/http2` this module already carries. It said to decide
// before writing a line of transport, and (a) is what was decided.
//
// The measurement that made (b) tempting stands: h2c really is free here. What
// is not free is everything above it, and that is where the argument turns.
// Option (b) meant hand-maintaining a decoder for SubscribeResponse, Notification
// and TypedValue against a spec this project does not own — and a decoder that is
// wrong does not fail, it produces a credible false graph. That is the exact
// failure this package's header says the whole design exists to avoid, so
// choosing (b) would have been spending the design's own principle to save a
// go.sum entry.
//
// The cost is real and is stated rather than minimised: `github.com/openconfig/
// gnmic/pkg/api` is ONE direct require — it is its own module, not the whole
// gnmic tool, which is the fact that made this cheaper than TODO.md assumed —
// but its transitive closure brings grpc, protobuf and openconfig/gnmi, and it
// pulls `golang.org/x/net` forward several releases for the whole project. That
// is now the largest thing meshcraft carries, and it has to be kept patched.
//
// # WHY THE ENCODING IS NAMED, AND WHY IT IS PROTO
//
// `SubscriptionList.encoding` defaults to JSON, and that default silently breaks
// this package. The paths it subscribes are CONTAINERS (`…/state/counters`),
// chosen so one notification carries a port and its ten leaves. Under PROTO a
// container arrives decomposed, one update per leaf with its own path — the shape
// [Samples] is written for. Under JSON it arrives as ONE update whose path stops
// at the container and whose value is an object; `leafSuffix` then yields
// "state/counters", which is in no table, and every counter is dropped with a
// healthy subscription and an empty graph to show for it.
//
// So PROTO is requested explicitly. [Expand] still handles the JSON shape,
// because a target is free to answer in an encoding other than the one asked
// for, and discovering that through empty graphs is not acceptable.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	gnmipb "github.com/openconfig/gnmi/proto/gnmi"
	gnmicapi "github.com/openconfig/gnmic/pkg/api"
)

// TransportReady says whether [Transport] can actually subscribe.
//
// True since the dependency decision was taken. It is still read by a
// registry-wide test in internal/api, which now checks the OTHER direction: a
// connector may declare a `gnmi` channel precisely because something subscribes.
// The constant stays rather than being deleted — it is what a future build that
// strips the gRPC stack would flip back, and the guard would come back with it.
const TransportReady = true

// dialTimeout bounds the gRPC connection setup.
//
// Separate from any subscription lifetime, and short: a target that does not
// complete a TCP+TLS handshake in this long is off, firewalled, or not serving
// gNMI at all, and the collector's supervisor should get its refusal quickly
// enough to back off rather than hold a goroutine against a dead address.
const dialTimeout = 10 * time.Second

// notifyBuffer sizes the channel handed back to the collector.
//
// Non-zero on purpose. A subscription to a 48-port switch produces a burst at
// every sample tick — one notification per port — and an unbuffered channel would
// make the gRPC reader wait on the translator for each one, which pushes back on
// the device's stream. 64 covers a full faceplate's burst without letting the
// queue become a second, unaccounted buffer: everything past it is the collector
// being genuinely too slow, and blocking there is the correct signal.
const notifyBuffer = 64

// grpcTransport is the real [Subscriber].
type grpcTransport struct{}

// Transport returns the real subscriber.
func Transport() Subscriber { return grpcTransport{} }

// Subscribe opens the gRPC stream and translates it into [Notification]s.
//
// The returned [Stream]'s channel is CLOSED when the stream ends, whatever ended
// it — context cancellation, a device that hung up, a gRPC error. That is the
// whole contract the collector relies on, and the reason the goroutine below has
// exactly one exit path.
//
// It also RECORDS what ended it, which it did not use to do, and that omission
// is the one that made core01.lab's hour of dark telemetry unreportable: the
// gRPC status naming the authentication refusal reached this goroutine, went to
// a Debug line, and was dropped. See [Stream].
func (grpcTransport) Subscribe(ctx context.Context, t Target,
	subs []Subscription) (*Stream, error) {

	if t.Username == "" || t.Password == "" {
		// Refused BEFORE dialling, and this is not pedantry. EOS authenticates a
		// subscriber against AAA; an anonymous Subscribe is rejected per attempt,
		// and a supervisor that retries would produce a failed-login line in the
		// switch's log every few seconds — which on a box with lockout policy is
		// how meshcraft would lock out its own service account.
		return nil, fmt.Errorf("gnmi target %s has no device login: a subscription "+
			"authenticates with a username and password, and retrying without one "+
			"would hammer the switch's AAA", t.DeviceID)
	}
	var opts, err = targetOptions(t)
	if err != nil {
		return nil, err
	}
	var tgt, terr = gnmicapi.NewTarget(opts...)
	if terr != nil {
		return nil, fmt.Errorf("gnmi target %s: %v", t.DeviceID, terr)
	}

	var req *gnmipb.SubscribeRequest
	req, err = subscribeRequest(subs)
	if err != nil {
		return nil, err
	}

	// The dial gets its own deadline, DERIVED from ctx rather than replacing it:
	// the subscription itself must live as long as ctx, and a timeout applied to
	// the whole thing would tear the stream down ten seconds in.
	var dialCtx, cancelDial = context.WithTimeout(ctx, dialTimeout)
	err = tgt.CreateGNMIClient(dialCtx)
	cancelDial()
	if err != nil {
		// WRAPPED WITH %w, not %v. The dial error carries a gRPC status when
		// there is one, and [ClassifyEnd] reads it through the chain — flattening
		// it to text here would turn every dial failure into [EndUnknown] for no
		// reason. (In practice this branch is rare: the gnmic client dials
		// lazily, so an unreachable device fails on the first Recv instead.)
		return nil, fmt.Errorf("gnmi dial %s: %w", t.DeviceID, err)
	}

	var rspCh, errCh = tgt.SubscribeStreamChan(ctx, req, t.DeviceID)
	var out = make(chan Notification, notifyBuffer)
	// endErr is written by the goroutine below BEFORE it closes `out`, and read
	// by the collector only after it has observed that close. That ordering is
	// the whole synchronisation: closing a channel happens-before the receive
	// that observes it, so no mutex is needed and none would add anything.
	var endErr error
	go func() {
		// ONE defer chain, and the order matters: close the channel last so a
		// collector selecting on it never sees a closed channel while the
		// connection is still being torn down. It also means every assignment to
		// endErr below is sequenced before the close that publishes it.
		defer close(out)
		defer tgt.Close() //nolint:errcheck
		var rsp *gnmipb.SubscribeResponse
		var open bool
		var serr error
		for {
			select {
			case <-ctx.Done():
				return
			case serr = <-errCh:
				if serr == nil {
					continue
				}
				// KEPT, and that is the change. The status this error carries is
				// the only place on the whole path that distinguishes "the device
				// refused the login" from "the device did not answer", and it was
				// being discarded one line below.
				endErr = serr
				// DEBUG and not WARN, deliberately, and it is the same judgement
				// cmd/proxy/snmp.go makes about a silent agent: a switch that
				// reboots, an interface that flaps, a management VLAN that moves
				// all end a subscription, and none of them is a fault worth a line
				// per occurrence across a fleet. The supervisor that resubscribes
				// is what decides when silence becomes a problem — and it can now
				// decide it, because the reason reaches it.
				slog.Debug("gnmi subscription ended", "device", t.DeviceID,
					"ip", t.IP, "err", serr.Error())
				return
			case rsp, open = <-rspCh:
				if !open {
					return
				}
				var upd = rsp.GetUpdate()
				if upd == nil {
					// A sync_response or a delete-only notification. Neither carries
					// a value, and neither is an error: sync_response is the device
					// saying it has finished sending the initial state.
					continue
				}
				select {
				case out <- notificationOf(upd):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return &Stream{C: out, Ended: func() error { return endErr }}, nil
}

// targetOptions turns a [Target] into the gnmic client's options.
//
// The TLS half is where the security decision this package documents actually
// takes effect, so it is written as one exhaustive branch rather than a set of
// independent flags: a target is EITHER plaintext or TLS, and there is no
// combination of fields that produces neither.
func targetOptions(t Target) ([]gnmicapi.TargetOption, error) {
	var port = t.Port
	if port <= 0 {
		port = DefaultPort
	}
	var opts = []gnmicapi.TargetOption{
		gnmicapi.Name(t.DeviceID),
		gnmicapi.Address(fmt.Sprintf("%s:%d", t.IP, port)),
		gnmicapi.Username(t.Username),
		gnmicapi.Password(t.Password),
		gnmicapi.Timeout(dialTimeout),
	}
	if t.Plaintext {
		// The login goes out in clear. Said once per subscription at WARN, and
		// not suppressed: this is opt-in, so somebody chose it, and the one thing
		// that must not happen is for that choice to become invisible once it is
		// working. A fleet where every Arista streams over plaintext should be
		// readable as such in the proxy's log.
		slog.Warn("gnmi subscription is PLAINTEXT: the device login travels in "+
			"clear in gRPC metadata on the management network",
			"device", t.DeviceID, "ip", t.IP)
		return append(opts, gnmicapi.Insecure(true)), nil
	}
	var cfg = &tls.Config{
		// TLS 1.2 floor. Not 1.3: these switches' embedded stacks do not all
		// carry it, and a floor that no target in the fleet can meet would push
		// every operator to Plaintext — which is the outcome this whole branch
		// exists to avoid.
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: t.SkipVerify, //nolint:gosec // opt-in, see Target.SkipVerify
	}
	var pem = strings.TrimSpace(t.CAPEM)
	if pem != "" {
		var pool = x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(pem)) {
			// REFUSED rather than falling back to the system pool. A CA that does
			// not parse means somebody pasted the wrong thing; carrying on would
			// verify against a pool that holds no switch issuer, fail every
			// handshake, and send them looking at the switch instead of at the
			// field they just filled in.
			return nil, fmt.Errorf("gnmi target %s: the CA supplied is not "+
				"PEM-encoded certificate material", t.DeviceID)
		}
		cfg.RootCAs = pool
	}
	return append(opts, gnmicapi.TLSConfig(cfg)), nil
}

// subscribeRequest builds the STREAM SubscriptionList.
//
// Built from the protobuf types directly rather than through gnmic's option
// chain, because the three fields this package had to get right — encoding,
// per-subscription mode, sample interval — are exactly the ones an option chain
// makes it easy to omit. Here, omitting one does not compile away quietly; it is
// visible in the literal.
func subscribeRequest(subs []Subscription) (*gnmipb.SubscribeRequest, error) {
	if len(subs) == 0 {
		return nil, fmt.Errorf("a subscription request with no path would open a " +
			"stream that never delivers anything")
	}
	var list = make([]*gnmipb.Subscription, 0, len(subs))
	var s Subscription
	for _, s = range subs {
		var one = &gnmipb.Subscription{Path: protoPath(s.Path)}
		switch s.Mode {
		case SubOnChange:
			one.Mode = gnmipb.SubscriptionMode_ON_CHANGE
			// No SampleInterval: on an ON_CHANGE subscription the field is
			// meaningless, and some targets reject a request that sets both.
		case SubSample:
			one.Mode = gnmipb.SubscriptionMode_SAMPLE
			if s.SampleInterval <= 0 {
				return nil, fmt.Errorf("a SAMPLE subscription on %s carries no "+
					"interval: the target would choose one and the declared "+
					"cadence would mean nothing", s.Path.String())
			}
			one.SampleInterval = uint64(s.SampleInterval.Nanoseconds())
		default:
			one.Mode = gnmipb.SubscriptionMode_TARGET_DEFINED
		}
		list = append(list, one)
	}
	return &gnmipb.SubscribeRequest{
		Request: &gnmipb.SubscribeRequest_Subscribe{
			Subscribe: &gnmipb.SubscriptionList{
				Subscription: list,
				Mode:         gnmipb.SubscriptionList_STREAM,
				// NAMED, never defaulted. See the file header: the default is JSON
				// and it silently empties every counter graph.
				Encoding: gnmipb.Encoding_PROTO,
				// UpdatesOnly false: we WANT the initial state dump. A collector
				// that only saw changes would show nothing for a port that has been
				// up and quiet since before the proxy started — which is most of a
				// healthy fleet.
				UpdatesOnly: false,
			},
		},
	}, nil
}

// protoPath converts this package's path into the wire one.
func protoPath(p Path) *gnmipb.Path {
	var elems = make([]*gnmipb.PathElem, 0, len(p))
	var e PathElem
	for _, e = range p {
		// The keys map is handed over as-is; `[name=*]` is the gNMI wildcard and
		// needs no special case — it is a key whose value is the literal "*".
		elems = append(elems, &gnmipb.PathElem{Name: e.Name, Key: e.Keys})
	}
	return &gnmipb.Path{Elem: elems}
}

// pathOf converts a wire path into this package's.
//
// Uses `Elem` and NOT the deprecated `Element` string list: the latter has no
// keys at all, so a path read from it would lose the port name and every sample
// built on it would be attributed to nothing. A target still speaking the old
// form yields an empty path here, which [Samples] drops — the correct outcome,
// and better than a plausible-looking sample with no port.
func pathOf(p *gnmipb.Path) Path {
	if p == nil {
		return nil
	}
	var out = make(Path, 0, len(p.GetElem()))
	var e *gnmipb.PathElem
	for _, e = range p.GetElem() {
		var keys map[string]string
		if len(e.GetKey()) > 0 {
			keys = make(map[string]string, len(e.GetKey()))
			var k, v string
			for k, v = range e.GetKey() {
				keys[k] = v
			}
		}
		out = append(out, PathElem{Name: e.GetName(), Keys: keys})
	}
	return out
}

// notificationOf converts a wire notification into the neutral one.
func notificationOf(n *gnmipb.Notification) Notification {
	var out = Notification{Prefix: pathOf(n.GetPrefix())}
	if n.GetTimestamp() > 0 {
		// The device's own clock, in nanoseconds since the epoch. Kept rather
		// than replaced by arrival time: it is what makes a stream worth having,
		// because it survives a queue on the proxy and a slow flush.
		out.TS = time.Unix(0, n.GetTimestamp()).UTC()
	}
	var u *gnmipb.Update
	for _, u = range n.GetUpdate() {
		out.Update = append(out.Update, Update{
			Path:  pathOf(u.GetPath()),
			Value: valueOf(u.GetVal()),
		})
	}
	// Deletes carried through rather than dropped. See [Notification.Delete]:
	// they are how a stream says a neighbour went away, and a collector that
	// ignored them would re-assert an unplugged cable for ever.
	var d *gnmipb.Path
	for _, d = range n.GetDelete() {
		out.Delete = append(out.Delete, pathOf(d))
	}
	return out
}

// valueOf converts a wire TypedValue into the neutral one.
//
// The default branch yields [ValueNone], which produces NO SAMPLE — never a
// zero. That is snmppoll.toFloat's rule and it matters most for the cases landing
// here: a leaflist, a byte blob, or a type added to the spec after this was
// written. A counter published as 0 because we could not read it reads as an idle
// link, which is a quiet lie; an absent sample reads as an absent sample.
func valueOf(tv *gnmipb.TypedValue) Value {
	if tv == nil {
		return Value{}
	}
	switch v := tv.GetValue().(type) {
	case *gnmipb.TypedValue_IntVal:
		return NumberValue(float64(v.IntVal))
	case *gnmipb.TypedValue_UintVal:
		// float64 holds an exact integer up to 2^53. A 64-bit octet counter can
		// exceed that after roughly 9 exabytes on one port, at which point the
		// value loses its low bits — but the series is a monotonic counter read
		// for its DERIVATIVE, and a rate computed from two nearby large values
		// stays correct. Named because the conversion looks lossy and, here, is
		// not in the way that matters.
		return NumberValue(float64(v.UintVal))
	case *gnmipb.TypedValue_DoubleVal:
		return NumberValue(v.DoubleVal)
	case *gnmipb.TypedValue_FloatVal:
		// Deprecated in the spec and still emitted by shipping firmware. Absent
		// from this switch it fell to ValueNone, i.e. to no sample at all.
		return NumberValue(float64(v.FloatVal))
	case *gnmipb.TypedValue_DecimalVal:
		// DECIMAL64, and this case was MISSING — found on a live 7050SX running
		// EOS 4.28.13.1M, which publishes every environmental sensor this way.
		//
		// The consequence was total and silent, which is this package's signature
		// failure mode: `/components/component[name=*]/state/temperature/instant`
		// arrived on every sample tick, decoded to ValueNone, produced no sample,
		// and `meshcraft_temperature_celsius` stayed empty for the whole fleet
		// with a healthy subscription behind it and nothing in any log. It was
		// found by dumping the leaves that arrive rather than the series that come
		// out — the two questions a mapped view cannot tell apart.
		//
		// The encoding is an integer and a decimal exponent: 41.5 travels as
		// digits=415, precision=1. Reconstructing it by division is exact for the
		// precisions these sensors use (0 to 3), and the type is deprecated in
		// favour of DoubleVal, so this will not need to grow.
		var d = v.DecimalVal
		if d == nil {
			return Value{}
		}
		var scale = math.Pow(10, float64(d.Precision))
		if scale == 0 {
			return Value{}
		}
		return NumberValue(float64(d.Digits) / scale)
	case *gnmipb.TypedValue_BoolVal:
		// 1/0 rather than a string: the health series that take a boolean
		// (meshcraft_psu_ok) are defined as 1 = healthy, and the rest of the fleet
		// fills them from SNMP integers.
		if v.BoolVal {
			return NumberValue(1)
		}
		return NumberValue(0)
	case *gnmipb.TypedValue_StringVal:
		return StringValue(v.StringVal)
	case *gnmipb.TypedValue_AsciiVal:
		return StringValue(v.AsciiVal)
	case *gnmipb.TypedValue_JsonVal:
		return JSONValue(v.JsonVal)
	case *gnmipb.TypedValue_JsonIetfVal:
		return JSONValue(v.JsonIetfVal)
	}
	return Value{}
}

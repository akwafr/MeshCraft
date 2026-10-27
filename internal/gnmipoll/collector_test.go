package gnmipoll

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/metricbuf"
)

// fakeSubscriber replays a fixed list of notifications and then closes, which is
// what a device does when a subscription ends. It exists so the whole collector
// loop — subscribe, translate, hand to the sink, stop — runs in a test with no
// wire, which is the entire reason [Subscriber] is an interface.
type fakeSubscriber struct {
	notes []Notification
	// closeAfter false leaves the channel open, so cancellation is what ends the
	// loop rather than the stream ending.
	closeAfter bool
	// seen records what the collector asked for.
	seen []Subscription
	err  error
	// endErr is the reason the stream ended, reported once the channel is closed
	// — what a real target's gRPC status becomes. Meaningful with closeAfter
	// only: a stream that has not ended has no reason to give.
	endErr error
}

func (f *fakeSubscriber) Subscribe(ctx context.Context, t Target,
	subs []Subscription) (*Stream, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.seen = subs
	var ch = make(chan Notification, len(f.notes))
	var n Notification
	for _, n = range f.notes {
		ch <- n
	}
	if f.closeAfter {
		close(ch)
	}
	return &Stream{C: ch, Ended: func() error { return f.endErr }}, nil
}

func counterNote(t *testing.T, port string, octets float64) Notification {
	t.Helper()
	return Notification{
		Prefix: mustParse(t, "/interfaces/interface[name="+port+"]"),
		TS:     time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC),
		Update: []Update{
			{Path: mustParse(t, "state/counters/in-octets"), Value: NumberValue(octets)},
		},
	}
}

// Notifications in, samples out through the sink, and the loop ends when the
// device closes the stream.
func TestCollectorRunFeedsTheSink(t *testing.T) {
	var sub = &fakeSubscriber{
		notes: []Notification{
			counterNote(t, "Ethernet1", 10),
			counterNote(t, "Ethernet2", 20),
		},
		closeAfter: true,
	}
	var c = Collector{
		Target: Target{DeviceID: "dev-1", IP: "192.0.2.5"},
		Metric: MetricIfCounters,
	}
	var got []metricbuf.Sample
	var err = c.Run(context.Background(), sub, func(list []metricbuf.Sample) {
		got = append(got, list...)
	})
	if err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("%d samples, want 2: %+v", len(got), got)
	}
	if got[0].Labels["device"] != "dev-1" {
		t.Errorf("the target's device id did not reach the sample: %v", got[0].Labels)
	}
	// The subscription set follows the metric, not a default: asking a device to
	// stream counters at the state cadence costs its CPU for nothing.
	if len(sub.seen) != 1 || sub.seen[0].Path.String() == "" {
		t.Fatalf("if_counters subscribed to %d paths, want the single counters container",
			len(sub.seen))
	}
	if sub.seen[0].SampleInterval == 0 {
		t.Error("counters were subscribed ON CHANGE: a counter that only reports on change " +
			"reports continuously on a busy port and never on an idle one")
	}
}

// A notification producing nothing does not wake the sink. An empty batch costs
// a lock on the proxy's shared buffer for nothing, and a device streaming leaves
// this build does not know would otherwise wake the whole pipeline on every
// update.
func TestCollectorRunDoesNotCallTheSinkForNothing(t *testing.T) {
	var sub = &fakeSubscriber{
		notes: []Notification{{
			Prefix: mustParse(t, "/system/state"),
			Update: []Update{{Path: mustParse(t, "hostname"), Value: StringValue("sw-1")}},
		}},
		closeAfter: true,
	}
	var c = Collector{Target: Target{DeviceID: "dev-1"}, Metric: MetricIfCounters}
	var calls int
	var err = c.Run(context.Background(), sub, func([]metricbuf.Sample) { calls++ })
	if err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if calls != 0 {
		t.Errorf("the sink was called %d times with nothing to deliver", calls)
	}
}

// Cancellation ends the loop even though the stream stays open — which is the
// case that matters, because a real subscription never ends on its own.
func TestCollectorRunStopsOnContextCancel(t *testing.T) {
	var sub = &fakeSubscriber{notes: []Notification{counterNote(t, "Ethernet1", 1)}}
	var c = Collector{Target: Target{DeviceID: "dev-1"}, Metric: MetricIfCounters}
	var ctx, cancel = context.WithCancel(context.Background())
	var done = make(chan error, 1)
	var seen = make(chan struct{}, 1)
	go func() {
		done <- c.Run(ctx, sub, func([]metricbuf.Sample) {
			select {
			case seen <- struct{}{}:
			default:
			}
		})
	}()
	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("the collector never delivered the queued notification")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the collector is still running after its context was cancelled")
	}
}

// The subscribe error is RETURNED, not swallowed: a collector that logged it and
// looped would retry a refusing device for ever.
func TestCollectorRunReturnsTheSubscribeError(t *testing.T) {
	var boom = errors.New("dial refused")
	var sub = &fakeSubscriber{err: boom}
	var c = Collector{Target: Target{DeviceID: "dev-1"}, Metric: MetricIfState}
	var err = c.Run(context.Background(), sub, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("Run returned %v, want the subscribe error", err)
	}
}

// THE TRANSPORT IS BUILT, and this guard now protects the opposite case from the
// one it was written for.
//
// It used to assert that nothing could subscribe, so that no connector could
// declare a channel nobody collected. The dependency decision has been taken
// (transport.go's header records which way and why), so the promise is now
// keepable — and what needs guarding is the reverse: a build that stripped the
// gRPC stack back out would flip this constant, and every connector declaring a
// `gnmi` channel would go quietly empty. The registry test in internal/api reads
// this same constant and is what actually catches that.
func TestTransportIsBuilt(t *testing.T) {
	if !TransportReady {
		t.Fatal("TransportReady is false: no connector may declare a gnmi monitoring " +
			"channel, and internal/api's registry guard must be the one enforcing it")
	}
}

// A TARGET WITH NO DEVICE LOGIN IS REFUSED BEFORE ANY SOCKET OPENS.
//
// The refusal is not politeness. EOS authenticates a subscriber against AAA, so
// an anonymous Subscribe is rejected per attempt — and the proxy's supervisor
// retries. That combination writes a failed-login line into the switch's log
// every few seconds, which on a box carrying a lockout policy is how meshcraft
// would lock out its own service account. Refusing without dialling is what makes
// the retry loop harmless.
func TestSubscribeRefusesATargetWithNoDeviceLogin(t *testing.T) {
	var ch, err = Transport().Subscribe(context.Background(),
		// A TEST-NET-1 address (RFC 5737): if the refusal ever regressed into a
		// dial, this must not reach anything real.
		Target{DeviceID: "dev-1", IP: "192.0.2.1"},
		SubscriptionsFor(MetricIfState, 0))
	if err == nil {
		t.Fatal("a target with no username or password was accepted")
	}
	if ch != nil {
		t.Error("the transport returned a channel alongside its refusal")
	}
	if !strings.Contains(err.Error(), "device login") {
		t.Errorf("the refusal does not name what is missing: %s", err)
	}
}

// if_state is subscribed ON CHANGE, which is the one thing a poll can never do
// and the reason streaming is worth having at all.
//
// The MODE is what is asserted, not the interval. Those are not the same claim:
// the mode used to be left at its zero value and inferred from a zero interval,
// which on the wire is TARGET_DEFINED — the device then answers at whatever
// cadence it likes, the graph still fills, and the port-down event this
// subscription exists to carry arrives late with nothing anywhere saying so.
func TestSubscriptionsForIfStateAreOnChange(t *testing.T) {
	var subs = SubscriptionsFor(MetricIfState, 0)
	if len(subs) != 2 {
		t.Fatalf("%d if_state subscriptions, want oper-status and port-speed", len(subs))
	}
	var s Subscription
	for _, s = range subs {
		if s.Mode != SubOnChange {
			t.Errorf("%s has mode %v, want SubOnChange", s.Path.String(), s.Mode)
		}
	}
}

// The CONTROLLER's cadence reaches the wire.
//
// It did not use to: `SubscriptionsFor` hard-coded 30 s, so a connector's
// declared MinInterval was resolved by the controller, written into the watch,
// and then ignored. Nothing failed — samples arrived — and the only symptom was
// that changing the declared floor changed nothing at all.
func TestCounterCadenceComesFromTheCaller(t *testing.T) {
	var subs = SubscriptionsFor(MetricIfCounters, 5*time.Second)
	if len(subs) != 1 {
		t.Fatalf("%d if_counters subscriptions, want one container", len(subs))
	}
	if subs[0].Mode != SubSample {
		t.Errorf("counters mode is %v, want SubSample — a counter on change reports "+
			"continuously on a busy port and never on an idle one", subs[0].Mode)
	}
	if subs[0].SampleInterval != 5*time.Second {
		t.Errorf("counter interval = %v, want the caller's 5s", subs[0].SampleInterval)
	}
	// And a caller that resolved nothing still gets a real cadence rather than a
	// SAMPLE subscription with no interval, which the target would answer at a
	// rate of its own choosing.
	subs = SubscriptionsFor(MetricIfCounters, 0)
	if subs[0].SampleInterval != DefaultSampleInterval {
		t.Errorf("unset interval = %v, want the named fallback", subs[0].SampleInterval)
	}
}

// THE ENCODING IS NAMED, and the request says STREAM.
//
// `SubscriptionList.encoding` defaults to JSON, and that default silently empties
// this package: it subscribes CONTAINERS, and under JSON a container arrives as
// one update whose value is an object — `leafSuffix` yields "state/counters",
// which is in no table, and every counter is dropped behind a healthy
// subscription. This is the assertion that keeps the field from being lost in a
// refactor, because nothing else would notice.
func TestSubscribeRequestNamesProtoEncoding(t *testing.T) {
	var req, err = subscribeRequest(SubscriptionsFor(MetricIfCounters, 10*time.Second))
	if err != nil {
		t.Fatalf("subscribeRequest: %v", err)
	}
	var list = req.GetSubscribe()
	if list == nil {
		t.Fatal("the request carries no SubscriptionList")
	}
	if list.GetEncoding().String() != "PROTO" {
		t.Errorf("encoding = %s, want PROTO", list.GetEncoding())
	}
	if list.GetMode().String() != "STREAM" {
		t.Errorf("list mode = %s, want STREAM", list.GetMode())
	}
	if list.GetUpdatesOnly() {
		t.Error("updates_only is set: a port up and quiet since before the proxy " +
			"started would never be reported, which is most of a healthy fleet")
	}
	var one = list.GetSubscription()[0]
	if one.GetMode().String() != "SAMPLE" {
		t.Errorf("counter sub mode = %s, want SAMPLE", one.GetMode())
	}
	if one.GetSampleInterval() != uint64(10*time.Second) {
		t.Errorf("sample interval = %d ns, want 10s", one.GetSampleInterval())
	}
	// The wildcard must survive the conversion to the wire path: it is what makes
	// ONE subscription cover every interface, where SNMP walks a table.
	var elem = one.GetPath().GetElem()
	var found bool
	var e = elem
	var i int
	for i = range e {
		if e[i].GetKey()["name"] == "*" {
			found = true
		}
	}
	if !found {
		t.Error("the interface wildcard did not survive protoPath")
	}
}

// A SAMPLE subscription with no interval is REFUSED rather than sent.
//
// Sent, the target picks a cadence and the declared floor becomes decorative —
// the same silent failure as the hard-coded interval, arriving by another door.
func TestSubscribeRequestRefusesASampleWithNoInterval(t *testing.T) {
	var _, err = subscribeRequest([]Subscription{{Path: pathCounters, Mode: SubSample}})
	if err == nil {
		t.Fatal("a SAMPLE subscription with no interval was accepted")
	}
}

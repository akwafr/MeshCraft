package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"meshcraft/internal/gnmipoll"
	"meshcraft/internal/tunnel"
)

// What this file tests: what a RE-PUSH of the watch list does to subscriptions
// that are already running.
//
// SetWatches reconciles rather than restarting, and the reason it does is sound —
// a WatchConfig comes down again on every controller-side change, and tearing
// every stream down each time would drop the accumulated LLDP tables and re-issue
// a fleet-wide initial-state dump for a change that touched nobody. So the
// property defended here is a NEGATIVE one, and negative properties are the ones
// that rot quietly: nothing fails when a reconcile becomes a restart, the fleet
// just costs more, and nobody notices for a release.
//
// The opposite failure is the one that put core01.lab's telemetry in the dark
// for over an hour on 2026-08-10, and it is the same code path read from the other
// side: reconciling EXISTENCE and nothing else means a re-push carrying a rotated
// password is absorbed and discarded, and the old goroutine goes on failing
// authentication at the 60 s backoff cap for ever. A restart that never happens
// is as silent as a restart that always happens.
//
// Both directions therefore need pinning, and pinning them costs a transport that
// records what it was asked to open — which is exactly what gnmipoll.Subscriber
// exists to allow (cf. its doc comment).

// recordingSubscriber is a Subscriber that never ends a stream on its own.
//
// It holds every subscription open until the caller's context is cancelled, which
// is what a healthy device does and what makes a teardown observable: nothing here
// closes except by cancellation, so a rise in `closed` IS a restart and cannot be
// a device hanging up.
type recordingSubscriber struct {
	mu     sync.Mutex
	opened []gnmiOpened
	closed int
}

// gnmiOpened is one subscribe call, kept whole: the parameters are the point of
// these tests, not the number of calls.
type gnmiOpened struct {
	target gnmipoll.Target
	subs   []gnmipoll.Subscription
}

func (r *recordingSubscriber) Subscribe(ctx context.Context, t gnmipoll.Target,
	subs []gnmipoll.Subscription) (*gnmipoll.Stream, error) {

	r.mu.Lock()
	r.opened = append(r.opened, gnmiOpened{target: t, subs: subs})
	r.mu.Unlock()
	var out = make(chan gnmipoll.Notification)
	go func() {
		<-ctx.Done()
		r.mu.Lock()
		r.closed++
		r.mu.Unlock()
		// The subscriber owns the channel and closes it on ctx, as the interface
		// requires.
		close(out)
	}()
	// No Ended: this fake only ever ends by cancellation, which is not a fault
	// and has no reason to report. gnmipoll.Stream.Reason is nil-safe precisely
	// so a fake like this needs no ceremony.
	return &gnmipoll.Stream{C: out}, nil
}

func (r *recordingSubscriber) count() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.opened), r.closed
}

func (r *recordingSubscriber) nth(i int) gnmiOpened {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.opened[i]
}

// gnmiTestCollector builds a collector wired to a recording transport and nothing
// else. No buffer is needed: the fake never delivers a notification, so nothing
// reaches AddAll.
func gnmiTestCollector() (*GNMICollector, *recordingSubscriber) {
	var rec = &recordingSubscriber{}
	var c = NewGNMICollector(nil, nil)
	c.sub = rec
	return c, rec
}

// waitOpened blocks until n subscriptions have been opened.
//
// Polling rather than a channel handshake because a subscription is opened by a
// supervisor GOROUTINE that SetWatches starts and does not wait for: there is no
// point in the API where "the push has been applied" is observable, and inventing
// one for the test would change the thing being tested.
func waitOpened(t *testing.T, rec *recordingSubscriber, n int) {
	t.Helper()
	var deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var got, _ = rec.count()
		if got >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	var got, closed = rec.count()
	t.Fatalf("%d subscription(s) opened after 2 s, expected %d (%d closed)",
		got, n, closed)
}

// settle gives a re-push time to do something wrong.
//
// Any assertion that a push did NOT restart has to wait, or it proves only that
// the restart had not happened YET. Short, because the only thing that could
// legitimately re-open a stream is a supervisor backing off, and gnmiBackoffMin is
// two seconds — an order of magnitude away.
func settle() { time.Sleep(80 * time.Millisecond) }

// gnmiWatchFixture is core01.lab as the controller pushes it: counters
// streamed at the cadence the EOS connector floors at, and nothing else, so one
// watch means exactly one subscription and a count is unambiguous.
func gnmiWatchFixture() tunnel.GNMIWatch {
	return tunnel.GNMIWatch{
		DeviceID:          "dev-core01",
		IP:                "192.0.2.5",
		Port:              6030,
		Username:          "mcadmin",
		Password:          "before-the-rotation",
		SkipVerify:        true,
		CountersIntervalS: 5,
	}
}

// TestRepushWithIdenticalParametersDoesNotRestart is the property the reconcile
// exists for, and it is asserted FIRST because the fix for the outage below is the
// obvious way to break it.
//
// The controller re-pushes the whole WatchConfig on any change to any of it. If
// renaming one device re-opened every stream on the fleet, every device would
// re-send its full initial state and every accumulated LLDP table would be reset —
// a cost proportional to the fleet for a change that touched one row.
func TestRepushWithIdenticalParametersDoesNotRestart(t *testing.T) {
	var c, rec = gnmiTestCollector()
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	c.SetWatches(ctx, []tunnel.GNMIWatch{gnmiWatchFixture()})
	waitOpened(t, rec, 1)

	// The same watch again, and then a third time: a re-push is not a rare event
	// and must be idempotent, not merely survivable once.
	c.SetWatches(ctx, []tunnel.GNMIWatch{gnmiWatchFixture()})
	c.SetWatches(ctx, []tunnel.GNMIWatch{gnmiWatchFixture()})
	settle()

	var opened, closed = rec.count()
	if opened != 1 || closed != 0 {
		t.Fatalf("%d subscription(s) opened and %d closed after two identical "+
			"re-pushes, expected 1 and 0: the reconcile has become a restart, and "+
			"every controller-side change now costs the fleet an initial-state dump",
			opened, closed)
	}
}

// TestCore01LabRotatedCredentialResubscribes is the regression for the outage
// of 2026-08-10, and it is named after it so the next reader knows what a failure
// here costs.
//
// The site's `gnmi` credential was rotated on core01.lab, mcadmin's password
// changed on the device, and the subscription started failing authentication. The
// corrected password was put back in the vault and the controller re-pushed the
// watch — logging `gnmi_targets:1, gnmi_skipped_no_credential:0`, i.e. reporting
// success. Nothing happened. SetWatches compared existence and only existence, saw
// the key running, and left the old goroutine retrying the stale password until it
// sat at the 60 s backoff cap. Telemetry was dark for over an hour; restarting the
// controller changed nothing, because the map that had swallowed the push lives on
// the PROXY.
//
// The device, the new credential and the collector were all provably healthy
// throughout: tools/labfwd + tools/labgnmi drove the product's own
// gnmipoll.Collector against 192.0.2.5:6030 with the new secret over the same SOCKS
// route and got 8 series / 325 samples back.
func TestCore01LabRotatedCredentialResubscribes(t *testing.T) {
	var c, rec = gnmiTestCollector()
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	c.SetWatches(ctx, []tunnel.GNMIWatch{gnmiWatchFixture()})
	waitOpened(t, rec, 1)

	var rotated = gnmiWatchFixture()
	rotated.Password = "after-the-rotation"
	c.SetWatches(ctx, []tunnel.GNMIWatch{rotated})
	waitOpened(t, rec, 2)

	var _, closed = rec.count()
	if closed != 1 {
		t.Errorf("%d subscription(s) closed, expected 1: the new stream was opened "+
			"but the old goroutine was left running, so the device now carries two "+
			"subscribers and one of them fails authentication for ever", closed)
	}
	if rec.nth(1).target.Password != "after-the-rotation" {
		t.Errorf("the resubscribe used password %q: it re-dialled with the stale "+
			"credential, which is the outage with an extra step",
			rec.nth(1).target.Password)
	}
}

// TestChangedUsernameResubscribes: the other half of a credential.
//
// Worth its own case because the two fields move independently — deploying a new
// service account changes the username and the password, but a fleet migrating
// from `admin` to `mcadmin` on an unchanged secret changes only the first.
func TestChangedUsernameResubscribes(t *testing.T) {
	var c, rec = gnmiTestCollector()
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	c.SetWatches(ctx, []tunnel.GNMIWatch{gnmiWatchFixture()})
	waitOpened(t, rec, 1)

	var renamed = gnmiWatchFixture()
	renamed.Username = "admin"
	c.SetWatches(ctx, []tunnel.GNMIWatch{renamed})
	waitOpened(t, rec, 2)

	if rec.nth(1).target.Username != "admin" {
		t.Errorf("resubscribed as %q, expected admin", rec.nth(1).target.Username)
	}
}

// TestChangedTransportTrustResubscribes: the TLS parameters are dial parameters
// too, and this one is a SECURITY change, not a cosmetic one.
//
// A site that turns Plaintext off has decided its device logins must stop crossing
// the management network in the clear (cf. gnmipoll.Target). Absorbing that push
// and leaving the plaintext stream up would mean the setting reads as applied on
// screen while the password keeps going out unencrypted — the exact inversion
// gnmipoll.Target's comment refuses to allow.
func TestChangedTransportTrustResubscribes(t *testing.T) {
	var c, rec = gnmiTestCollector()
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	var clear = gnmiWatchFixture()
	clear.Plaintext = true
	c.SetWatches(ctx, []tunnel.GNMIWatch{clear})
	waitOpened(t, rec, 1)

	var secured = gnmiWatchFixture()
	secured.Plaintext = false
	c.SetWatches(ctx, []tunnel.GNMIWatch{secured})
	waitOpened(t, rec, 2)

	if rec.nth(1).target.Plaintext {
		t.Errorf("resubscribed in plaintext: the site's decision to encrypt was " +
			"reported as applied and was not")
	}
}

// TestChangedCountersIntervalResubscribes: a cadence that reaches the wire is a
// parameter like any other.
//
// A subscription cannot be re-parameterised in place — the interval is sent once,
// at subscribe time, and the device samples at it until the stream ends. So the
// only way to apply a re-resolved cadence is to re-subscribe, and NOT applying it
// reproduces the bug internal/gnmipoll's header warns about: a connector's declared
// floor that changes nothing at all, with a stream still arriving and nothing to
// say the number was ignored.
func TestChangedCountersIntervalResubscribes(t *testing.T) {
	var c, rec = gnmiTestCollector()
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	c.SetWatches(ctx, []tunnel.GNMIWatch{gnmiWatchFixture()})
	waitOpened(t, rec, 1)

	var slower = gnmiWatchFixture()
	slower.CountersIntervalS = 30
	c.SetWatches(ctx, []tunnel.GNMIWatch{slower})
	waitOpened(t, rec, 2)

	// Read off the wire rather than off the watch: the assertion is that the DEVICE
	// was asked to sample at the new cadence, which is the only thing the change
	// was for.
	var subs = rec.nth(1).subs
	if len(subs) != 1 || subs[0].SampleInterval != 30*time.Second {
		t.Fatalf("resubscribed with %+v, expected one SAMPLE subscription at 30 s",
			subs)
	}
}

// TestChangedOnChangeIntervalDoesNotResubscribe is the judgement call in
// gnmiParamsMoved, pinned.
//
// `if_state` is subscribed ON CHANGE. Its number in the watch is read as a boolean
// by metricsOf and is never sent — gnmipoll.SubscriptionsFor ignores the interval
// for the on-change sets. So 30 → 60 changes NOTHING the device could observe, and
// restarting for it would reset an LLDP table and re-dump a full initial state to
// apply a difference that does not exist on the wire.
//
// This is why the comparison stores intervalOf's answer per KEY rather than the
// watch's four fields: the naive fix restarts every subscription of a device
// whenever any one of its cadences moves.
func TestChangedOnChangeIntervalDoesNotResubscribe(t *testing.T) {
	var c, rec = gnmiTestCollector()
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	var w = gnmiWatchFixture()
	w.CountersIntervalS = 0
	w.StateIntervalS = 30
	c.SetWatches(ctx, []tunnel.GNMIWatch{w})
	waitOpened(t, rec, 1)

	w.StateIntervalS = 60
	c.SetWatches(ctx, []tunnel.GNMIWatch{w})
	settle()

	var opened, closed = rec.count()
	if opened != 1 || closed != 0 {
		t.Fatalf("%d opened / %d closed after an on-change cadence went 30 -> 60: "+
			"the stream was restarted to apply a number that is never sent",
			opened, closed)
	}
}

// TestIntervalChangeRestartsOnlyTheMetricItReaches: the same rule, stated on the
// blast radius rather than on the wire.
//
// A device streaming counters AND neighbours has its counters cadence re-resolved.
// The LLDP subscription must not move: runLLDP resets the accumulated table on
// every resubscribe, so restarting it here would discard a neighbour view to apply
// a cadence that belongs to another subscription entirely.
func TestIntervalChangeRestartsOnlyTheMetricItReaches(t *testing.T) {
	var c, rec = gnmiTestCollector()
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	var w = gnmiWatchFixture()
	w.LLDPIntervalS = 30
	c.SetWatches(ctx, []tunnel.GNMIWatch{w})
	waitOpened(t, rec, 2)

	w.CountersIntervalS = 10
	c.SetWatches(ctx, []tunnel.GNMIWatch{w})
	waitOpened(t, rec, 3)
	settle()

	var opened, closed = rec.count()
	if opened != 3 || closed != 1 {
		t.Fatalf("%d opened / %d closed, expected 3 and 1: a counters cadence change "+
			"must restart the counters subscription and nothing else — the LLDP "+
			"stream carries an accumulated table that a resubscribe resets",
			opened, closed)
	}
}

// TestCredentialChangeRestartsEveryMetricOfTheDevice: the converse, and the reason
// the comparison is not scoped to one subscription.
//
// A rotated password invalidates every stream of that device, including the
// neighbour one. Here the LLDP table IS worth resetting: it is not being discarded
// to apply a cadence, it is being reopened because nothing else can arrive at all.
func TestCredentialChangeRestartsEveryMetricOfTheDevice(t *testing.T) {
	var c, rec = gnmiTestCollector()
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	var w = gnmiWatchFixture()
	w.LLDPIntervalS = 30
	c.SetWatches(ctx, []tunnel.GNMIWatch{w})
	waitOpened(t, rec, 2)

	w.Password = "after-the-rotation"
	c.SetWatches(ctx, []tunnel.GNMIWatch{w})
	waitOpened(t, rec, 4)

	var _, closed = rec.count()
	if closed != 2 {
		t.Fatalf("%d closed, expected 2: both of the device's subscriptions "+
			"authenticate with the rotated credential and both must be reopened",
			closed)
	}
}

// TestWatchRemovedStopsSubscription: the half that already worked, kept because the
// stop loop was rewritten around the new map value and a stop that stops nothing
// leaves a proxy subscribing to a device it has been told to forget.
func TestWatchRemovedStopsSubscription(t *testing.T) {
	var c, rec = gnmiTestCollector()
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	c.SetWatches(ctx, []tunnel.GNMIWatch{gnmiWatchFixture()})
	waitOpened(t, rec, 1)

	c.SetWatches(ctx, nil)
	var deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var _, closed = rec.count()
		if closed == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	var opened, closed = rec.count()
	if opened != 1 || closed != 1 {
		t.Fatalf("%d opened / %d closed after the watch was withdrawn, expected 1 "+
			"and 1", opened, closed)
	}
	c.mu.Lock()
	var left = len(c.subs)
	c.mu.Unlock()
	if left != 0 {
		t.Errorf("%d entr(ies) left in subs: cancelled but not deleted, so the key "+
			"blocks any future subscription of that device", left)
	}
}

// TestCredentialGoneEmptyStopsSubscription: a watch that arrives without a login is
// not "unchanged", it is a subscription that must END.
//
// This is what a `gnmi` site credential being deleted or failing to resolve looks
// like on the wire, and the controller says so explicitly — it counts those devices
// as `gnmi_skipped_no_credential`. Leaving the old stream up would keep telemetry
// flowing on a login the controller no longer vouches for, and would make the
// controller's own count a lie about what the proxy is doing.
func TestCredentialGoneEmptyStopsSubscription(t *testing.T) {
	var c, rec = gnmiTestCollector()
	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	c.SetWatches(ctx, []tunnel.GNMIWatch{gnmiWatchFixture()})
	waitOpened(t, rec, 1)

	var stripped = gnmiWatchFixture()
	stripped.Password = ""
	c.SetWatches(ctx, []tunnel.GNMIWatch{stripped})
	var deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var _, closed = rec.count()
		if closed == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	var opened, closed = rec.count()
	if closed != 1 {
		t.Errorf("%d closed, expected 1: the watch lost its credential and the "+
			"stream kept running on the old one", closed)
	}
	if opened != 1 {
		t.Errorf("%d opened, expected 1: a watch with no login must not be "+
			"subscribed to at all", opened)
	}
	c.mu.Lock()
	var left = len(c.subs)
	c.mu.Unlock()
	if left != 0 {
		t.Errorf("%d entr(ies) left in subs for a device with no credential", left)
	}
}

// TestGNMIParamsMovedNamesTheClass checks what the log line will SAY, without a
// collector.
//
// The class names are the whole operator-facing product of this fix — they are what
// turns "the curves stopped" into "the credential moved, go and look at the site's
// gnmi credential" — and they are the one thing a test of behaviour does not
// observe.
func TestGNMIParamsMovedNamesTheClass(t *testing.T) {
	var base = gnmiSub{
		target:   targetOfGNMI(gnmiWatchFixture()),
		interval: 5 * time.Second,
	}
	var cases = []struct {
		name string
		mut  func(*gnmiSub)
		want string
	}{
		{"unchanged", func(s *gnmiSub) {}, ""},
		{"password", func(s *gnmiSub) { s.target.Password = "x" }, "credential"},
		{"username", func(s *gnmiSub) { s.target.Username = "x" }, "credential"},
		{"ip", func(s *gnmiSub) { s.target.IP = "192.0.2.6" }, "address"},
		{"port", func(s *gnmiSub) { s.target.Port = 6031 }, "address"},
		{"plaintext", func(s *gnmiSub) { s.target.Plaintext = true }, "transport"},
		{"ca", func(s *gnmiSub) { s.target.CAPEM = "-----BEGIN" }, "transport"},
		{"interval", func(s *gnmiSub) { s.interval = 30 * time.Second }, "interval"},
		// Several at once is the ordinary case of a device being re-homed, and the
		// order must be stable: a log line whose fields reorder between incidents
		// cannot be grepped for.
		{"redeployed", func(s *gnmiSub) {
			s.target.IP = "192.0.2.6"
			s.target.Password = "x"
			s.interval = 30 * time.Second
		}, "address,credential,interval"},
	}
	var tc = cases[0]
	for _, tc = range cases {
		var next = base
		tc.mut(&next)
		var got = gnmiParamsMoved(base, next)
		if got != tc.want {
			t.Errorf("%s: gnmiParamsMoved = %q, expected %q", tc.name, got, tc.want)
		}
	}
}

// TestGNMIParamsMovedNeverCarriesAValue: the class names must stay CLASS names.
//
// Journals get shipped off the proxy, and a gNMI login on this fleet configures the
// switch rather than merely reading it (cf. tunnel.GNMIWatch). Interpolating the
// changed field "for diagnosis" would put a device password in a log aggregator,
// and it would be a one-line change made in good faith by someone who did not know
// that — hence an assertion rather than a comment.
func TestGNMIParamsMovedNeverCarriesAValue(t *testing.T) {
	const secret = "a-very-recognisable-secret"
	var before = gnmiSub{target: targetOfGNMI(gnmiWatchFixture())}
	var after = before
	after.target.Password = secret
	after.target.Username = secret
	after.target.IP = secret
	after.target.CAPEM = secret

	var got = gnmiParamsMoved(before, after)
	if got == "" {
		t.Fatal("gnmiParamsMoved found nothing on a watch where everything moved")
	}
	if strings.Contains(got, secret) {
		t.Fatalf("gnmiParamsMoved = %q: it carries the changed VALUE, and the log "+
			"line built from it goes wherever the proxy's journal goes", got)
	}
}

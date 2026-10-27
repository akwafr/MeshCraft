package store

// Alerting storage tests that need PostgreSQL — ADR-0024.
//
// Separation is the same as rekey_test.go, and deliberate: the POLICY is tested
// without a database (internal/api/alerting_test.go), and what is tested here
// is the part that only PostgreSQL can answer. Three properties, none of which
// a simulation proves:
//
//   - the partial UNIQUE index really does permit at most one held slip per
//     (channel, key), so coalescing is correct under concurrency rather than
//     merely likely;
//   - `least(release, first_at + cap)` really does floor the hold, so a port
//     that flaps forever still speaks;
//   - the re-arm UPDATE really does skip a key that already holds a slip,
//     instead of hitting the unique index and rolling the whole batch back.
//
// Run exactly as rekey_test.go documents (disposable database, delete after):
//
//	docker network create meshcraft-test
//	docker run -d --name meshcraft-testdb --network meshcraft-test \
//	  -e POSTGRES_USER=t -e POSTGRES_PASSWORD=t -e POSTGRES_DB=t postgres:17-alpine
//	docker run --rm --network meshcraft-test -v $PWD:/src -w /src \
//	  -e MESHCRAFT_TEST_DATABASE_URL='postgres://t:t@meshcraft-testdb:5432/t?sslmode=disable' \
//	  golang:1.24-alpine go test -count=1 ./internal/store/...
//	docker rm -f meshcraft-testdb && docker network rm meshcraft-test

import (
	"context"
	"testing"
	"time"

	"meshcraft/internal/model"
)

// alertFixture makes a site, a device and a channel to hang slips off.
func alertFixture(t *testing.T, s *Store) (string, string, string) {
	t.Helper()
	var ctx = context.Background()
	var siteID string
	var err = s.pool.QueryRow(ctx,
		`insert into sites (name, slug) values ('alerting-test',
		   'alerting-test-' || substr(md5(random()::text), 1, 8))
		 returning id::text`).Scan(&siteID)
	if err != nil {
		t.Fatalf("site: %v", err)
	}
	var deviceID string
	err = s.pool.QueryRow(ctx,
		`insert into devices (site_id, name) values ($1::uuid, 'sw-test')
		 returning id::text`, siteID).Scan(&deviceID)
	if err != nil {
		t.Fatalf("device: %v", err)
	}
	var ch model.NotifyChannel
	ch, err = s.UpsertNotifyChannel(ctx, model.NotifyChannel{
		SiteID: siteID, Kind: model.NotifyKindTelegram, Label: "test",
		Target: "-1001", Enabled: true, MinSeverity: model.SevNotice,
		Classes: []string{model.AlertClassDevice, model.AlertClassInterco,
			model.AlertClassDescribed},
	}, []byte("ciphertext"))
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	return siteID, deviceID, ch.ID
}

func insertTestEvent(t *testing.T, s *Store, siteID, deviceID, port, typ string) int64 {
	t.Helper()
	var id, err = s.InsertEventID(context.Background(), model.Event{
		SiteID: &siteID, DeviceID: &deviceID, PortName: port,
		Type: typ, Severity: model.SevInfo, Message: "test", Actor: "monitoring",
	})
	if err != nil {
		t.Fatalf("event: %v", err)
	}
	return id
}

// THE ANTI-FLAP, exercised against the real index. Six transitions on one port
// must leave ONE held slip carrying a count of six — not six slips, and not a
// unique-violation error.
func TestFileDeliveryGroupsTheFlapping(t *testing.T) {
	var s, _ = testStore(t)
	var ctx = context.Background()
	var siteID, deviceID, chanID = alertFixture(t, s)
	var key = model.AlertClassInterco + "|" + deviceID + "|GE1"

	var now = time.Now()
	var i int
	var last model.NotifyDelivery
	for i = 0; i < 6; i++ {
		var evID = insertTestEvent(t, s, siteID, deviceID, "GE1", "port.down")
		var d, err = s.FileDelivery(ctx, evID, chanID, key,
			now.Add(time.Duration(i)*20*time.Second).Add(45*time.Second),
			5*time.Minute)
		if err != nil {
			t.Fatalf("transition %d: %v", i, err)
		}
		last = d
	}

	// One held slip, counting all six.
	var held int
	var err = s.pool.QueryRow(ctx,
		`select count(*) from notify_deliveries
		 where channel_id = $1::uuid and alert_key = $2 and state = 'held'`,
		chanID, key).Scan(&held)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if held != 1 {
		t.Fatalf("%d slips held for one key, expected 1: "+
			"six transitions would have produced six messages", held)
	}
	if last.Transitions != 6 {
		t.Fatalf("transitions = %d, expected 6 — the message would not say "+
			"how many times the port moved", last.Transitions)
	}
	// And the five absorbed events each kept a slip, so every journal line
	// remains traceable to a decision.
	var coalesced int
	err = s.pool.QueryRow(ctx,
		`select count(*) from notify_deliveries
		 where channel_id = $1::uuid and alert_key = $2 and state = 'coalesced'`,
		chanID, key).Scan(&coalesced)
	if err != nil {
		t.Fatalf("count coalesced: %v", err)
	}
	if coalesced != 5 {
		t.Fatalf("%d slips absorbed, expected 5", coalesced)
	}
}

// The cap is enforced in SQL against the slip's stored first_at, because only
// the database knows when the episode began — the process filing the tenth
// transition may not be the one that filed the first.
func TestFileDeliveryCapsTheHold(t *testing.T) {
	var s, _ = testStore(t)
	var ctx = context.Background()
	var siteID, deviceID, chanID = alertFixture(t, s)
	var key = model.AlertClassDescribed + "|" + deviceID + "|GE2"
	var cap = 5 * time.Minute

	var first = insertTestEvent(t, s, siteID, deviceID, "GE2", "port.down")
	var opened, err = s.FileDelivery(ctx, first, chanID, key,
		time.Now().Add(45*time.Second), cap)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Ask for a deadline an hour out — far past the ceiling.
	var evID = insertTestEvent(t, s, siteID, deviceID, "GE2", "port.up")
	var d model.NotifyDelivery
	d, err = s.FileDelivery(ctx, evID, chanID, key, time.Now().Add(time.Hour), cap)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	var ceiling = opened.FirstAt.Add(cap)
	if d.ReleaseAt.After(ceiling.Add(time.Second)) {
		t.Fatalf("deadline %v beyond the cap %v: a port that flaps forever "+
			"would never speak", d.ReleaseAt, ceiling)
	}
}

// Two different keys must NOT absorb each other: a device alert and a port
// alert on the same device are different stories.
func TestFileDeliverySeparatesKeys(t *testing.T) {
	var s, _ = testStore(t)
	var ctx = context.Background()
	var siteID, deviceID, chanID = alertFixture(t, s)

	var a = insertTestEvent(t, s, siteID, deviceID, "", "device.unreachable")
	var b = insertTestEvent(t, s, siteID, deviceID, "GE1", "port.down")
	var _, err = s.FileDelivery(ctx, a, chanID,
		model.AlertClassDevice+"|"+deviceID+"|", time.Now(), 5*time.Minute)
	if err != nil {
		t.Fatalf("device: %v", err)
	}
	_, err = s.FileDelivery(ctx, b, chanID,
		model.AlertClassInterco+"|"+deviceID+"|GE1", time.Now(), 5*time.Minute)
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	var held int
	err = s.pool.QueryRow(ctx,
		`select count(*) from notify_deliveries
		 where channel_id = $1::uuid and state = 'held'`, chanID).Scan(&held)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if held != 2 {
		t.Fatalf("%d slips held, expected 2: two distinct stories "+
			"would have absorbed each other", held)
	}
}

// Re-arming must SKIP a key that already holds a slip rather than violating the
// partial unique index — which would roll back the whole batch and silently
// block every other re-arm in it.
func TestReArmDoesNotBreakOnAnAlreadyHeldKey(t *testing.T) {
	var s, _ = testStore(t)
	var ctx = context.Background()
	var siteID, deviceID, chanID = alertFixture(t, s)
	var key = model.AlertClassDevice + "|" + deviceID + "|"

	// A suppressed slip, and a held slip on the SAME key.
	var supp = insertTestEvent(t, s, siteID, deviceID, "", "device.unreachable")
	var err = s.SuppressDelivery(ctx, supp, chanID, key, "device.stp_apply")
	if err != nil {
		t.Fatalf("suppress: %v", err)
	}
	var suppID int64
	err = s.pool.QueryRow(ctx,
		`select id from notify_deliveries where event_id = $1`, supp).Scan(&suppID)
	if err != nil {
		t.Fatalf("read suppressed: %v", err)
	}
	var newer = insertTestEvent(t, s, siteID, deviceID, "", "device.unreachable")
	_, err = s.FileDelivery(ctx, newer, chanID, key, time.Now(), 5*time.Minute)
	if err != nil {
		t.Fatalf("file: %v", err)
	}

	// Re-arming the suppressed one must not error, and must leave it alone.
	err = s.ReArmSuppressed(ctx, []int64{suppID}, time.Now())
	if err != nil {
		t.Fatalf("re-arm failed instead of skipping the key: %v", err)
	}
	var state string
	err = s.pool.QueryRow(ctx,
		`select state from notify_deliveries where id = $1`, suppID).Scan(&state)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state != model.NotifySuppressed {
		t.Fatalf("state %q: the key already carried a held slip, "+
			"that one is the message", state)
	}
}

// A suppressed alert whose window has closed comes back — that is what makes
// suppression a DELAY and not a loss. Without it, an outage starting four
// minutes after an apply would be silenced by that apply for good.
func TestReArmReturnsADeferredAlert(t *testing.T) {
	var s, _ = testStore(t)
	var ctx = context.Background()
	var siteID, deviceID, chanID = alertFixture(t, s)
	var key = model.AlertClassDevice + "|" + deviceID + "|"

	var evID = insertTestEvent(t, s, siteID, deviceID, "", "device.unreachable")
	// Backdate the journal line past the window.
	var _, err = s.pool.Exec(ctx,
		`update events set ts = now() - interval '30 minutes' where id = $1`, evID)
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if err = s.SuppressDelivery(ctx, evID, chanID, key, "config.applied"); err != nil {
		t.Fatalf("suppress: %v", err)
	}

	var due, lookupErr = s.SuppressedPastWindow(ctx, time.Now(), 10*time.Minute, 50)
	if lookupErr != nil {
		t.Fatalf("lookup: %v", lookupErr)
	}
	var found bool
	var x DeliveryWithEvent
	for _, x = range due {
		if x.Event.ID == evID {
			found = true
		}
	}
	if !found {
		t.Fatal("an alert suppressed outside the window must become a candidate again: " +
			"otherwise suppression is a loss, not a postponement")
	}

	// Re-armed, it is held and will be delivered.
	if err = s.ReArmSuppressed(ctx, []int64{due[0].Delivery.ID}, time.Now()); err != nil {
		t.Fatalf("re-arm: %v", err)
	}
	var state, reason string
	err = s.pool.QueryRow(ctx,
		`select state, suppressed_reason from notify_deliveries where id = $1`,
		due[0].Delivery.ID).Scan(&state, &reason)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if state != model.NotifyHeld {
		t.Fatalf("state %q, expected %q", state, model.NotifyHeld)
	}
	// The reason keeps its history: "suppressed because X, then re-armed" is
	// the sentence somebody reads months later.
	if reason == "config.applied" {
		t.Fatal("the re-arming must stay readable in the reason")
	}
	// And a re-armed slip is not offered again, or the sweep would loop on it.
	var again []DeliveryWithEvent
	again, err = s.SuppressedPastWindow(ctx, time.Now(), 10*time.Minute, 50)
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	for _, x = range again {
		if x.Delivery.ID == due[0].Delivery.ID {
			t.Fatal("a re-armed slip is offered again: the sweep would loop")
		}
	}
}

// The list route is secret-free BY CONSTRUCTION — notifyChanCols never selects
// token_enc. This proves the column list, not the handler's good intentions.
func TestTheListNeverReturnsTheToken(t *testing.T) {
	var s, _ = testStore(t)
	var ctx = context.Background()
	var siteID, _, chanID = alertFixture(t, s)

	var list, err = s.ListNotifyChannels(ctx, siteID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	var c model.NotifyChannel
	for _, c = range list {
		if c.ID == chanID {
			found = true
			if !c.HasToken {
				t.Fatal("has_token must say the secret exists")
			}
		}
	}
	if !found {
		t.Fatal("the site's channel must appear in its list")
	}
	// The reveal path is the ONLY one that returns ciphertext, and it returns
	// ciphertext — decryption and the audit line belong to the handler.
	var _, enc, revealErr = s.RevealNotifyToken(ctx, chanID)
	if revealErr != nil {
		t.Fatalf("reveal: %v", revealErr)
	}
	if string(enc) != "ciphertext" {
		t.Fatalf("the reveal must return the ciphertext verbatim, returned %q", enc)
	}
}

// Editing a channel WITHOUT a token must not wipe the stored one. The screen
// cannot read the token back, so it cannot resubmit it — without this rule,
// renaming a channel destroys its credential.
func TestEditingWithoutTokenDoesNotDestroyTheSecret(t *testing.T) {
	var s, _ = testStore(t)
	var ctx = context.Background()
	var siteID, _, chanID = alertFixture(t, s)

	var updated, err = s.UpsertNotifyChannel(ctx, model.NotifyChannel{
		ID: chanID, SiteID: siteID, Kind: model.NotifyKindTelegram,
		Label: "renamed", Target: "-1002", Enabled: true,
		MinSeverity: model.SevWarning,
		Classes:     []string{model.AlertClassInterco},
	}, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Label != "renamed" || updated.MinSeverity != model.SevWarning {
		t.Fatalf("the update did not take: %+v", updated)
	}
	if !updated.HasToken {
		t.Fatal("renaming a channel destroyed its token")
	}
	var _, enc, revealErr = s.RevealNotifyToken(ctx, chanID)
	if revealErr != nil {
		t.Fatalf("reveal: %v", revealErr)
	}
	if string(enc) != "ciphertext" {
		t.Fatalf("the stored token changed: %q", enc)
	}
}

// A declared link makes a port an interco. The observed half (an LLDP
// neighbour that resolves to a fleet device) is derived in internal/api and
// cannot be answered in SQL.
func TestPortFacesKnownDeviceLitLesLiensDeclares(t *testing.T) {
	var s, _ = testStore(t)
	var ctx = context.Background()
	var siteID, deviceID, _ = alertFixture(t, s)

	var other string
	var err = s.pool.QueryRow(ctx,
		`insert into devices (site_id, name) values ($1::uuid, 'sw-peer')
		 returning id::text`, siteID).Scan(&other)
	if err != nil {
		t.Fatalf("peer: %v", err)
	}

	var faces bool
	faces, err = s.PortFacesKnownDevice(ctx, siteID, deviceID, "GE1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if faces {
		t.Fatal("no declared link: the port must not be an interco")
	}

	_, err = s.pool.Exec(ctx,
		`insert into device_links (site_id, a_device, a_port, b_device, b_port)
		 values ($1::uuid, $2::uuid, 'GE1', $3::uuid, 'GE48')`,
		siteID, deviceID, other)
	if err != nil {
		t.Fatalf("link: %v", err)
	}

	faces, err = s.PortFacesKnownDevice(ctx, siteID, deviceID, "GE1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !faces {
		t.Fatal("a link declared on this port makes it an interco")
	}
	// Both ends count: the link is undirected, and the far device's own port
	// must be recognised as an interco too.
	faces, err = s.PortFacesKnownDevice(ctx, siteID, other, "GE48")
	if err != nil {
		t.Fatalf("lookup far: %v", err)
	}
	if !faces {
		t.Fatal("the other end of the cable is an interco too")
	}
}

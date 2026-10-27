package debuglog

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRingBoundsMemory(t *testing.T) {
	var r = NewRing(3)
	var i int
	for i = 1; i <= 10; i++ {
		r.Add(Line{TS: time.Now(), Level: "INFO", Msg: "line"})
	}
	var got = r.Snapshot("")
	if len(got) != 3 {
		t.Fatalf("the ring must bound to 3 lines, %d kept", len(got))
	}
	// From oldest to newest: a log that goes backward in time can't be read.
	if got[0].Seq != 8 || got[2].Seq != 10 {
		t.Errorf("unexpected order: %d → %d", got[0].Seq, got[2].Seq)
	}
}

func TestSnapshotFiltersByLevel(t *testing.T) {
	var r = NewRing(10)
	r.Add(Line{TS: time.Now(), Level: "INFO", Msg: "a"})
	r.Add(Line{TS: time.Now(), Level: "ERROR", Msg: "b"})
	var got = r.Snapshot("WARN")
	if len(got) != 1 || got[0].Msg != "b" {
		t.Errorf("level filtering is broken: %+v", got)
	}
}

// TestHandlerMasksSecrets: this stream is viewed in a browser. A password
// that transits through it is a leaked password, and the filtering is based
// on the attribute NAME — looking for a secret in a value would require
// knowing what it looks like, which is false by construction.
func TestHandlerMasksSecrets(t *testing.T) {
	var r = NewRing(10)
	var h = NewHandler(slog.NewJSONHandler(discard{}, nil), r)
	var log = slog.New(h)

	log.Info("login", "user", "mcadmin", "password", "SuperSecret1",
		"secret_key", "abc", "api_token", "xyz", "device", "sw-labo")

	var got = r.Snapshot("")
	if len(got) != 1 {
		t.Fatalf("expected 1 line, got %d", len(got))
	}
	var a = got[0].Attrs
	if a["password"] != "[masked]" || a["secret_key"] != "[masked]" ||
		a["api_token"] != "[masked]" {
		t.Errorf("a secret leaked: %+v", a)
	}
	// What isn't sensitive must stay readable, otherwise the log is useless.
	if a["device"] != "sw-labo" || a["user"] != "mcadmin" {
		t.Errorf("useful attributes must be preserved: %+v", a)
	}
}

// TestHandlerDoesNotDivertTheStream: logs must keep going to standard
// output. A debug layer that captured the stream would lose the container's
// logs at the exact moment they're needed most.
func TestHandlerDoesNotDivertTheStream(t *testing.T) {
	var out = &capture{}
	var r = NewRing(10)
	slog.New(NewHandler(slog.NewJSONHandler(out, nil), r)).Warn("warning")
	if !strings.Contains(out.String(), "warning") {
		t.Errorf("the next handler must still receive the line: %q", out.String())
	}
	if len(r.Snapshot("")) != 1 {
		t.Error("and so must the ring")
	}
}

// TestSubscribeIsBounded: each stream holds a channel and an HTTP goroutine.
// A forgotten browser tab must not cost resources forever.
func TestSubscribeIsBounded(t *testing.T) {
	var r = NewRing(10)
	r.maxSubs = 2
	var _, stop1, ok1 = r.Subscribe()
	var _, stop2, ok2 = r.Subscribe()
	var _, _, ok3 = r.Subscribe()
	if !ok1 || !ok2 {
		t.Fatal("the first two subscriptions must succeed")
	}
	if ok3 {
		t.Error("beyond the bound, the subscription must be refused")
	}
	stop1()
	stop2()
	var _, stop4, ok4 = r.Subscribe()
	if !ok4 {
		t.Error("releasing a subscription must free up a slot")
	}
	stop4()
}

// TestAddDoesNotBlockOnASlowSubscriber: a browser on a poor link must never
// slow down the code that's logging. It loses lines, which is the right
// tradeoff for debugging.
func TestAddDoesNotBlockOnASlowSubscriber(t *testing.T) {
	var r = NewRing(10)
	var _, stop, _ = r.Subscribe()
	defer stop()
	var done = make(chan struct{})
	go func() {
		var i int
		for i = 0; i < 1000; i++ {
			r.Add(Line{TS: time.Now(), Level: "INFO", Msg: "flood"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Add blocks when a subscriber isn't consuming")
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

type capture struct{ b strings.Builder }

func (c *capture) Write(p []byte) (int, error) { return c.b.Write(p) }
func (c *capture) String() string              { return c.b.String() }

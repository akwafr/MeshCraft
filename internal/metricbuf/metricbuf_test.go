package metricbuf

import (
	"sync"
	"testing"
	"time"
)

func s(name string, v float64) Sample {
	return Sample{Name: name, Value: v, TS: time.Now()}
}

// TestAtLeastOnceDelivery is THE reason this buffer exists. A tunnel drop at
// the wrong moment must not lose the in-flight batch.
func TestAtLeastOnceDelivery(t *testing.T) {
	var b = New(100)
	b.AddAll([]Sample{s("a", 1), s("b", 2), s("c", 3)})

	var batch = b.Take(2)
	if len(batch) != 2 || batch[0].Name != "a" {
		t.Fatalf("unexpected batch: %+v", batch)
	}
	// Send fails: the batch returns, at the HEAD, to preserve chronology.
	b.Nack()
	batch = b.Take(3)
	if len(batch) != 3 || batch[0].Name != "a" || batch[2].Name != "c" {
		t.Errorf("after Nack, everything must return in order: %+v", batch)
	}
	b.Ack()
	var got = b.Take(10)
	if len(got) != 0 {
		t.Errorf("after Ack the buffer must be empty: %+v", got)
	}
}

// TestSingleInFlightBatch: two in-flight batches would deliver points out of
// order on the first error.
func TestSingleInFlightBatch(t *testing.T) {
	var b = New(100)
	b.AddAll([]Sample{s("a", 1), s("b", 2)})
	var first = b.Take(1)
	var second = b.Take(1)
	if len(second) != 1 || second[0].Name != first[0].Name {
		t.Errorf("Take must return the same batch until settled: %+v / %+v",
			first, second)
	}
}

// TestBoundDropsOldest: a tunnel down all night must not fill proxy memory — a
// dead proxy supervises nothing. And on a metric, the recent point always beats
// the stale one.
func TestBoundDropsOldest(t *testing.T) {
	var b = New(3)
	var i int
	for i = 1; i <= 6; i++ {
		b.Add(Sample{Name: "m", Value: float64(i), TS: time.Now()})
	}
	var batch = b.Take(10)
	if len(batch) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(batch))
	}
	if batch[0].Value != 4 || batch[2].Value != 6 {
		t.Errorf("the most RECENT must remain: %v %v", batch[0].Value, batch[2].Value)
	}
	// Drops must be counted: monitoring that drops without saying so lies.
	var st = b.Stats()
	if st.Dropped != 3 {
		t.Errorf("expected 3 drops, got %d", st.Dropped)
	}
}

// TestNackDoesNotEvictNewer: returning a batch must not evict newer points —
// that would invert the rule we just set.
func TestNackDoesNotEvictNewer(t *testing.T) {
	var b = New(3)
	b.AddAll([]Sample{s("v1", 1), s("v2", 2)})
	var batch = b.Take(2)
	if len(batch) != 2 {
		t.Fatalf("expected batch of 2")
	}
	// While the batch is in flight, collection continues.
	b.AddAll([]Sample{s("n1", 3), s("n2", 4), s("n3", 5)})
	b.Nack()

	var got = b.Take(10)
	if len(got) != 3 {
		t.Fatalf("buffer stays bounded at 3, got %d", len(got))
	}
	// The two returned are the oldest: they are the ones dropped.
	if got[0].Name != "n1" || got[2].Name != "n3" {
		t.Errorf("recent points must survive: %+v", got)
	}
}

func TestStats(t *testing.T) {
	var b = New(10)
	b.AddAll([]Sample{s("a", 1), s("b", 2), s("c", 3)})
	b.Take(2)
	var st = b.Stats()
	if st.Queued != 1 || st.Pending != 2 || st.Cap != 10 {
		t.Errorf("unexpected stats: %+v", st)
	}
}

// TestConcurrency: collection writes while send drains. The buffer is the
// rendezvous point for both; it must hold.
func TestConcurrency(t *testing.T) {
	var b = New(5000)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		var i int
		for i = 0; i < 2000; i++ {
			b.Add(s("m", float64(i)))
		}
	}()
	go func() {
		defer wg.Done()
		var i int
		for i = 0; i < 200; i++ {
			b.Take(10)
			b.Ack()
		}
	}()
	var done = make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock between collection and send")
	}
}

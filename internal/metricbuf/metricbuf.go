// Package metricbuf is the proxy metrics buffer.
//
// The proxy collects continuously; the tunnel to the controller may drop. Without
// a buffer, monitoring loses exactly the minutes of an outage — that is, it lies
// at the most interesting moment. See
// docs/en/01-Architecture/Unified monitoring and pipelines.md section 2.
//
// IN MEMORY, and that is a deliberate choice. Writing every sample to disk would,
// on a proxy producing hundreds per second, cost more than it saves: a lost metric
// is re-sampled five seconds later, unlike a configuration change that never
// comes back on its own. The buffer therefore survives a TUNNEL outage, not a
// process restart — and saying so is better than implying durability that does
// not exist.
package metricbuf

import (
	"sync"

	"meshcraft/wire/metrics"
)

// Sample is a measurement point in the format VictoriaMetrics ingests — the
// wire type, aliased: producers on both sides of the driver boundary emit the
// same shape.
type Sample = metrics.Sample

// Stats reports what the buffer has experienced. `Dropped` is the value that
// matters: monitoring that drops points without saying so is monitoring that lies.
type Stats struct {
	Queued  int    `json:"queued"`
	Pending int    `json:"pending"`
	Dropped uint64 `json:"dropped"`
	Cap     int    `json:"cap"`
}

// Buffer is a bounded queue with AT-LEAST-ONCE delivery.
//
// The cycle is Take -> Ack or Nack: until the controller confirms, samples stay
// reserved and return to the head if the send fails. Without this reservation, a
// drop at the wrong moment would lose the in-flight batch — precisely the case
// this buffer exists to cover.
type Buffer struct {
	mu      sync.Mutex
	items   []Sample
	pending []Sample
	max     int
	dropped uint64
}

// New builds a buffer holding at most `max` samples.
//
// The bound is not stylistic caution: a tunnel down all night would fill the
// proxy memory, and a dead proxy supervises nothing. Better to drop the OLDEST
// points and continue.
func New(max int) *Buffer {
	if max <= 0 {
		max = 50000
	}
	return &Buffer{max: max}
}

// Add enqueues a sample. When the buffer is full, the OLDEST is dropped: on a
// metric, the recent point always beats the stale one.
func (b *Buffer) Add(s Sample) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) >= b.max {
		var over = len(b.items) - b.max + 1
		b.items = b.items[over:]
		b.dropped += uint64(over)
	}
	b.items = append(b.items, s)
}

// AddAll enqueues a batch.
func (b *Buffer) AddAll(list []Sample) {
	var s Sample
	for _, s = range list {
		b.Add(s)
	}
}

// Take reserves up to `max` samples for a send.
//
// Only one batch may be in flight at a time: calling Take while a batch is
// neither acknowledged nor returned yields that SAME batch. Two simultaneous
// in-flight batches would deliver points out of order on the first error.
func (b *Buffer) Take(max int) []Sample {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) > 0 {
		return append([]Sample{}, b.pending...)
	}
	if max <= 0 || max > len(b.items) {
		max = len(b.items)
	}
	if max == 0 {
		return nil
	}
	b.pending = append([]Sample{}, b.items[:max]...)
	b.items = b.items[max:]
	return append([]Sample{}, b.pending...)
}

// Ack confirms the batch arrived: it can be forgotten.
func (b *Buffer) Ack() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending = nil
}

// Nack returns the batch to the buffer, at the HEAD, to preserve chronological
// order.
//
// If the return overflows, the oldest still go — including among those being
// returned. Keeping the in-flight batch at the expense of newer points would
// invert the rule we just set.
func (b *Buffer) Nack() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) == 0 {
		return
	}
	b.items = append(b.pending, b.items...)
	b.pending = nil
	if len(b.items) > b.max {
		var over = len(b.items) - b.max
		b.items = b.items[over:]
		b.dropped += uint64(over)
	}
}

// Stats returns the buffer state.
func (b *Buffer) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Stats{
		Queued: len(b.items), Pending: len(b.pending),
		Dropped: b.dropped, Cap: b.max,
	}
}

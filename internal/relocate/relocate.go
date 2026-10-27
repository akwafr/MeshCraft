// Package relocate finds a device that changed address.
//
// The use case is management addressing cutover: after the push, the device
// reappears in another subnet, at an address the controller does not know (DHCP
// lease). Finding it again is essential — otherwise the cutover "succeeds" and
// the device is lost
// (docs/en/01-Architecture/Management addressing.md §5).
//
// Two paths run IN PARALLEL, and the first to succeed wins:
//
//  1. an automatic sweep of the target subnet;
//  2. an operator-entered address, which they often know before the sweep
//     finishes (lease visible on their DHCP server, device screen…).
//
// In both cases, identity is verified on the SERIAL NUMBER, never on the
// address: an address recycled by DHCP may designate another device, and
// pointing meshcraft at the wrong hardware would be worse than finding nothing.
package relocate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// State describes search progress.
type State string

const (
	StateSearching State = "searching"
	StateFound     State = "found"
	StateNotFound  State = "not_found"
	StateCancelled State = "cancelled"
	StateFailed    State = "failed"
)

// Status is what the UI shows during the search.
type Status struct {
	DeviceID string `json:"device_id"`
	// Serial: the identity being searched for. It is authoritative.
	Serial string `json:"serial"`
	Prefix string `json:"prefix"`
	State  State  `json:"state"`
	// Progress: how many addresses examined out of how many.
	Scanned int `json:"scanned"`
	Total   int `json:"total"`
	// Found*: filled when the device is found again.
	FoundIP string `json:"found_ip,omitempty"`
	// FoundBy says WHO found it: the sweep or the operator. The screen must be
	// able to say so, if only to know whether the sweep is useful.
	FoundBy   string     `json:"found_by,omitempty"` // scan | operator
	Error     string     `json:"error,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// Probe is what the tracker can do with an address: examine it and extract a
// serial number. The implementation lives in the API (it has the probe and
// connectors); we do not want to depend on it here.
type Probe func(ctx context.Context, ip string) (serial string, err error)

// OnFound is called when the device is found again: that is where the management
// address is updated and the event logged.
type OnFound func(deviceID, ip, foundBy string)

// ErrNotSearching: no search in progress for this device.
var ErrNotSearching = errors.New("no search in progress for this device")

// ErrSerialMismatch: something responds, but it is not the right device.
var ErrSerialMismatch = errors.New("serial number mismatch")

type search struct {
	mu     sync.Mutex
	status Status
	cancel context.CancelFunc
	probe  Probe
	found  OnFound
}

// Tracker holds searches in progress. One per device: restarting replaces the
// previous one.
type Tracker struct {
	mu       sync.RWMutex
	searches map[string]*search
}

func NewTracker() *Tracker {
	return &Tracker{searches: map[string]*search{}}
}

// Start launches a device search across a list of addresses.
func (t *Tracker) Start(deviceID, serial, prefix string, addrs []string,
	concurrency int, probe Probe, found OnFound) Status {
	t.Cancel(deviceID)

	var ctx, cancel = context.WithCancel(context.Background())
	var s = &search{
		status: Status{
			DeviceID: deviceID, Serial: serial, Prefix: prefix,
			State: StateSearching, Total: len(addrs), StartedAt: time.Now(),
		},
		cancel: cancel, probe: probe, found: found,
	}
	t.mu.Lock()
	t.searches[deviceID] = s
	t.mu.Unlock()

	go s.run(ctx, addrs, concurrency)
	return s.snapshot()
}

func (s *search) run(ctx context.Context, addrs []string, concurrency int) {
	defer func() {
		s.mu.Lock()
		if s.status.State == StateSearching {
			s.status.State = StateNotFound
			var now = time.Now()
			s.status.EndedAt = &now
		}
		s.mu.Unlock()
	}()
	if concurrency <= 0 {
		concurrency = 32
	}
	var wg sync.WaitGroup
	var slots = make(chan struct{}, concurrency)
	var ip string
	for _, ip = range addrs {
		if ctx.Err() != nil {
			return
		}
		wg.Add(1)
		slots <- struct{}{}
		go func(target string) {
			defer wg.Done()
			defer func() { <-slots }()
			var serial, err = s.probe(ctx, target)
			s.mu.Lock()
			s.status.Scanned++
			s.mu.Unlock()
			if err != nil || serial == "" {
				return
			}
			s.settle(target, serial, "scan")
		}(ip)
	}
	wg.Wait()
}

// settle records the discovery if the serial matches. Idempotent: two paths may
// succeed at once, only one must win.
func (s *search) settle(ip, serial, by string) bool {
	s.mu.Lock()
	if s.status.State != StateSearching {
		s.mu.Unlock()
		return false
	}
	if !sameSerial(serial, s.status.Serial) {
		s.mu.Unlock()
		return false
	}
	s.status.State = StateFound
	s.status.FoundIP = ip
	s.status.FoundBy = by
	var now = time.Now()
	s.status.EndedAt = &now
	var cb = s.found
	var deviceID = s.status.DeviceID
	s.mu.Unlock()

	// The sweep still in flight no longer has a reason to continue.
	s.cancel()
	if cb != nil {
		cb(deviceID, ip, by)
	}
	return true
}

// sameSerial compares without being fooled by case or spaces: devices are not
// consistent between their API and web pages.
func sameSerial(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// Submit proposes an address found by the operator. It is VERIFIED like the
// others: if the serial does not match, it is refused — the operator can pick
// the wrong address, and the controller must not follow blindly.
func (t *Tracker) Submit(ctx context.Context, deviceID, ip string) (Status, error) {
	var s = t.get(deviceID)
	if s == nil {
		return Status{}, ErrNotSearching
	}
	var serial, err = s.probe(ctx, ip)
	if err != nil {
		return s.snapshot(), err
	}
	if serial == "" {
		return s.snapshot(), errors.New("no identifiable device at this address")
	}
	if !s.settle(ip, serial, "operator") {
		var st = s.snapshot()
		if st.State == StateFound {
			// The sweep won in the meantime: this is not an error.
			return st, nil
		}
		return st, ErrSerialMismatch
	}
	return s.snapshot(), nil
}

// Cancel stops a search.
func (t *Tracker) Cancel(deviceID string) {
	var s = t.get(deviceID)
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.status.State == StateSearching {
		s.status.State = StateCancelled
		var now = time.Now()
		s.status.EndedAt = &now
	}
	s.mu.Unlock()
	s.cancel()
}

// Get returns the state of a search.
func (t *Tracker) Get(deviceID string) (Status, bool) {
	var s = t.get(deviceID)
	if s == nil {
		return Status{}, false
	}
	return s.snapshot(), true
}

func (t *Tracker) get(deviceID string) *search {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.searches[deviceID]
}

func (s *search) snapshot() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

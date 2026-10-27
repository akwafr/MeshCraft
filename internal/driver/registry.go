package driver

import (
	"encoding/json"
	"sort"
	"sync"
)

// --- Registry ---

var (
	registryMu sync.RWMutex
	registry   = map[string]Driver{}
)

// Register registers a connector. Called from the init of connector packages.
func Register(d Driver) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[d.Info().Name] = d
}

// Unregister removes a connector from the registry. A name that is not
// registered is a no-op, not an error: the caller unloading a driver instance
// cannot know whether a crashed instance already fell out.
//
// Compile-time drivers never unregister; this exists for externally loaded
// drivers whose lifetime is a container's, not the process's.
func Unregister(name string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	delete(registry, name)
}

// Get returns a connector by name.
func Get(name string) (Driver, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	var d, ok = registry[name]
	return d, ok
}

// Names lists every REGISTERED connector, sorted.
//
// Distinct from discovery.Drivers(), which enumerates FINGERPRINTS — what
// meshcraft knows how to recognize on a network. This enumerates what it knows
// how to talk to, which is the question a capability check has to ask: "does
// every connector that claims a web transport also say where that interface
// answers?" is only meaningful over the registry.
//
// Sorted so a test that iterates reports its failures in a stable order rather
// than a different one per run.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	var out = make([]string, 0, len(registry))
	var name string
	for name = range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// DriverNameFromExtra reads devices.extra.driver without failing when Extra also
// carries nested objects (stp_observed, …). Unmarshalling Extra into
// map[string]string rejects those shapes and used to make the connector look
// unknown (addressing apply shown as ANNOTATION only).
func DriverNameFromExtra(raw json.RawMessage) string {
	return ExtraString(raw, "driver")
}

// ExtraString reads a string field from devices.extra. Nested objects under other
// keys (stp_observed, …) are ignored — they must not make the whole decode fail.
func ExtraString(raw json.RawMessage, key string) string {
	if len(raw) == 0 || key == "" {
		return ""
	}
	var bag map[string]json.RawMessage
	if json.Unmarshal(raw, &bag) != nil {
		return ""
	}
	var v, ok = bag[key]
	if !ok || len(v) == 0 || string(v) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return ""
	}
	return s
}

// List returns all connectors, by name.
func List() []Driver {
	registryMu.RLock()
	defer registryMu.RUnlock()
	var out = make([]Driver, 0, len(registry))
	var d Driver
	for _, d = range registry {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Info().Name < out[j].Info().Name })
	return out
}

// Best picks the connector with the highest score above the threshold. Returns
// ok=false if nothing matches — the device then shows up as unidentified rather
// than forced into a range it isn't part of.
func Best(ev Evidence, threshold int) (Driver, int, bool) {
	var best Driver
	var bestScore int
	var d Driver
	for _, d = range List() {
		var score = d.Fingerprint(ev)
		if score > bestScore {
			best, bestScore = d, score
		}
	}
	if best == nil || bestScore < threshold {
		return nil, bestScore, false
	}
	return best, bestScore, true
}

// Unused keeps context in the signature of the upcoming contract (ReadState,
// Plan, Apply) without imposing a dependency starting now.

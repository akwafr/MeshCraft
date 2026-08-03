package api

import (
	"encoding/json"

	"meshcraft/internal/driver"
)

const planKeySTPPreference = "stp_mode_preference"

// stpPreferenceOf extracts the ordered STP mode preference from a site plan blob.
//
// Absent key → product default (rstp, stp, none).
// Explicit empty array → empty preferred list (everything stays Available).
func stpPreferenceOf(plan json.RawMessage) []string {
	if len(plan) == 0 {
		return append([]string{}, driver.DefaultSTPPreference...)
	}
	var parsed struct {
		Pref *[]string `json:"stp_mode_preference"`
	}
	if json.Unmarshal(plan, &parsed) != nil {
		return append([]string{}, driver.DefaultSTPPreference...)
	}
	if parsed.Pref == nil {
		return append([]string{}, driver.DefaultSTPPreference...)
	}
	return driver.SanitizeSTPPreference(*parsed.Pref)
}

// mergeSTPPreference writes preference into the site plan JSON without dropping
// other keys (zones, device positions, …). An empty list is stored as-is so the
// operator can clear Preferred and rebuild it (including re-adding STP).
func mergeSTPPreference(plan json.RawMessage, pref []string) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if len(plan) == 0 {
		m = map[string]json.RawMessage{}
	} else if json.Unmarshal(plan, &m) != nil {
		m = map[string]json.RawMessage{}
	}
	var clean = driver.SanitizeSTPPreference(pref)
	if clean == nil {
		clean = []string{}
	}
	var raw, err = json.Marshal(clean)
	if err != nil {
		return nil, err
	}
	m[planKeySTPPreference] = raw
	return json.Marshal(m)
}

// portSTPIntents extracts SpanningTreePortIntent from devices.ports JSON.
func portSTPIntents(raw json.RawMessage) []driver.SpanningTreePortIntent {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var names = make([]string, 0, len(m))
	var name string
	for name = range m {
		names = append(names, name)
	}
	// Stable order for transcripts.
	var i, j int
	for i = 1; i < len(names); i++ {
		for j = i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	var out []driver.SpanningTreePortIntent
	for _, name = range names {
		var fields = m[name]
		var mode string
		_ = json.Unmarshal(fields["mode"], &mode)
		if mode == "" || mode == "libre" {
			continue
		}
		var stpRaw, present = fields["stp"]
		if !present {
			continue
		}
		var spec struct {
			Edge      json.RawMessage `json:"edge"`
			Enabled   *bool           `json:"enabled"`
			BPDUGuard *bool           `json:"bpdu_guard"`
			PathCost  *int            `json:"path_cost"`
		}
		if json.Unmarshal(stpRaw, &spec) != nil {
			continue
		}
		var edge = parseSTPEdgeJSON(spec.Edge)
		if spec.Enabled == nil && edge == "" && spec.BPDUGuard == nil && spec.PathCost == nil {
			continue
		}
		out = append(out, driver.SpanningTreePortIntent{
			Port: name, Edge: edge, Enabled: spec.Enabled,
			BPDUGuard: spec.BPDUGuard, PathCost: spec.PathCost,
		})
	}
	return out
}

// parseSTPEdgeJSON accepts "auto"|"yes"|"no" or JSON bool true/false.
func parseSTPEdgeJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var asStr string
	if json.Unmarshal(raw, &asStr) == nil {
		return driver.NormalizeSTPEdge(asStr)
	}
	var asBool bool
	if json.Unmarshal(raw, &asBool) == nil {
		if asBool {
			return driver.STPEdgeYes
		}
		return driver.STPEdgeNo
	}
	return ""
}

// Package metrics is the series vocabulary meshcraft speaks: the sample shape
// VictoriaMetrics ingests, and the name of every series any producer emits —
// the proxy's SNMP poller, the controller's gNMI translator, and the drivers,
// in-tree or in their own containers. names.go carries the conventions and
// the reasoning; this file carries the one type.
package metrics

import "time"

// Sample is a measurement point in the format VictoriaMetrics ingests.
type Sample struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  float64           `json:"value"`
	TS     time.Time         `json:"ts"`
}

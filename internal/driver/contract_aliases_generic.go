package driver

// Generic functions cannot be re-exported through a `var` alias the way the
// rest of contract_aliases.go does — Go has no uninstantiated generic values —
// so the handful of them get ordinary forwarding wrappers here, by hand.

import "meshcraft/wire/mdpv1"

// SortByPortName forwards to the contract's implementation; see mdpv1's doc.
func SortByPortName[T any](items []T, port func(T) string, split PortNameSplit) {
	mdpv1.SortByPortName(items, port, split)
}

package api

import (
	"testing"

	"meshcraft/internal/model"
)

// The three model strings twelve fielded Instant On 1930 switches actually
// arrived under — two vendor naming grammars, three forms, one switch.
var instantOn1930Forms = []string{
	"Aruba Instant On 1930 48G 4SFP/SFP+ Switch",
	"HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 10G 1930",
	"HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A",
}

// What this locks is the WIRING, not the derivation (internal/modelid tests
// that). ADR-0014 names the risk explicitly: the identity is derived at two
// points — this one and publicDevicesOf — and a payload that skips them makes
// the duplicate reappear silently, on screen, with nothing failing anywhere.
// So both points are pinned, by the only property that matters to the operator
// who reported it: the three forms come out as ONE model.
func TestDeviceReadDerivesOneIdentityForThreeGrammars(t *testing.T) {
	var keys = map[string]bool{}
	var labels = map[string]bool{}
	var raw string
	for _, raw = range instantOn1930Forms {
		var out = withSyncVLANs(model.Device{Model: raw}, nil)
		if out.Model != raw {
			t.Errorf("Model = %q, want %q: the declared label is evidence and "+
				"is never rewritten", out.Model, raw)
		}
		if out.ModelKey == "" || out.ModelLabel == "" {
			t.Fatalf("%q: no identity derived on a device read (key %q, label %q)",
				raw, out.ModelKey, out.ModelLabel)
		}
		keys[out.ModelKey] = true
		labels[out.ModelLabel] = true
	}
	if len(keys) != 1 || len(labels) != 1 {
		t.Errorf("three grammars produced %d keys and %d labels, want 1 and 1: "+
			"%v / %v", len(keys), len(labels), keys, labels)
	}
}

// The catalogue reference travels, and only where the label carried one.
func TestDeviceReadCarriesTheCatalogueReference(t *testing.T) {
	var with = withSyncVLANs(model.Device{Model: instantOn1930Forms[2]}, nil)
	if with.ModelSKU != "JL685A" {
		t.Errorf("ModelSKU = %q, want JL685A", with.ModelSKU)
	}
	var without = withSyncVLANs(model.Device{Model: instantOn1930Forms[0]}, nil)
	if without.ModelSKU != "" {
		t.Errorf("ModelSKU = %q: that form carries none, and none is invented",
			without.ModelSKU)
	}
}

// The public projection is the other derivation point, and it feeds the SAME
// fleet table component as the authenticated screens. Without the call it would
// be the one page still naming one switch three ways.
func TestPublicProjectionCarriesTheIdentity(t *testing.T) {
	var devices []model.Device
	var raw string
	for _, raw = range instantOn1930Forms {
		devices = append(devices, model.Device{Model: raw})
	}
	var out = publicDevicesOf(devices)
	var labels = map[string]bool{}
	var d publicDevice
	var i int
	for i, d = range out {
		if d.Model != instantOn1930Forms[i] {
			t.Errorf("Model = %q, want %q", d.Model, instantOn1930Forms[i])
		}
		if d.ModelLabel == "" {
			t.Fatalf("%q: public projection without an identity", d.Model)
		}
		labels[d.ModelLabel] = true
	}
	if len(labels) != 1 {
		t.Errorf("public projection produced %d labels, want 1: %v", len(labels), labels)
	}
}

// A device with no model at all stays empty: an absent label must not acquire a
// phantom identity that every unnamed device would then share.
func TestNoModelStaysWithoutIdentity(t *testing.T) {
	var out = withSyncVLANs(model.Device{}, nil)
	if out.ModelKey != "" || out.ModelLabel != "" || out.ModelSKU != "" {
		t.Errorf("empty model got identity %q / %q / %q",
			out.ModelKey, out.ModelLabel, out.ModelSKU)
	}
}

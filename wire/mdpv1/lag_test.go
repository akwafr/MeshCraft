package mdpv1

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// LACP fallback is the one LAG field whose ABSENCE of support must be loud.
// These tests pin the three things that make it so: the VLAN is validated as a
// VLAN, it is meaningless on a static aggregate, and the capability is a
// method-set fact a connector cannot forget to announce.

func TestValidateLAGsFallbackVLANRange(t *testing.T) {
	var cases = []struct {
		name     string
		fallback int
		mode     string
		wantErr  string
	}{
		{"off is always fine", LAGFallbackOff, LAGModeStatic, ""},
		{"a VLAN with LACP", 42, LAGModeLACP, ""},
		{"VLAN 4094 is the last one", 4094, LAGModeLACP, ""},
		{"negative", -1, LAGModeLACP, "outside 1..4094"},
		{"above the range", 4095, LAGModeLACP, "outside 1..4094"},
		{"static cannot fall back", 42, LAGModeStatic, "never negotiates"},
	}
	var c struct {
		name     string
		fallback int
		mode     string
		wantErr  string
	}
	for _, c = range cases {
		var err = ValidateLAGs([]LAGState{{
			Name: "LAG1", Mode: c.mode, Members: []string{"1"},
			Fallback: c.fallback,
		}})
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected refusal: %v", c.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: accepted, expected a refusal mentioning %q", c.name, c.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: refusal %q does not mention %q", c.name, err, c.wantErr)
		}
	}
}

func TestLAGFallbackWanted(t *testing.T) {
	var none = []LAGState{
		{Name: "LAG1", Mode: LAGModeLACP, Members: []string{"1"}},
		{Name: "LAG2", Mode: LAGModeStatic, Members: []string{"2"}},
	}
	var _, want = LAGFallbackWanted(none)
	if want {
		t.Fatal("no aggregate asked for fallback, yet one was reported")
	}
	var some = []LAGState{
		{Name: "LAG1", Mode: LAGModeLACP, Members: []string{"1"}},
		{Name: "LAG2", Mode: LAGModeLACP, Members: []string{"2"}, Fallback: 99},
	}
	var got, had = LAGFallbackWanted(some)
	if !had || got.Name != "LAG2" || got.Fallback != 99 {
		t.Fatalf("expected LAG2 with fallback 99, got %+v (found=%v)", got, had)
	}
}

// RefuseLAGFallback must reach the operator as a CAPABILITY refusal, not a
// device error: that is what carries it over the wire as ErrCodeUnsupported
// with the gesture named.
func TestRefuseLAGFallbackIsACapabilityRefusal(t *testing.T) {
	var err = RefuseLAGFallback("hpe-procurve", "LAG3")
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatal("the refusal must unwrap to ErrNotImplemented, like every NotSupported")
	}
	var body = ErrorBodyOf(err)
	if body.Code != ErrCodeUnsupported {
		t.Fatalf("wire code %q, want %q", body.Code, ErrCodeUnsupported)
	}
	if !strings.Contains(body.Gesture, "LAG3") {
		t.Errorf("the gesture must name the aggregate, got %q", body.Gesture)
	}
	if body.Because == "" {
		t.Error("because is shown verbatim to operators and must not be empty")
	}
}

// A connector whose ApplyLAGs honours Fallback announces lag-fallback from its
// method set alone — the whole point of the marker, since there is no type
// assertion across a wire.
type fallbackWriter struct{ Admin }

func (fallbackWriter) ApplyLAGs(context.Context, []LAGState) (ApplyResult, error) {
	return ApplyResult{}, nil
}
func (fallbackWriter) WritesLAGFallback() {}

type plainLAGWriter struct{ Admin }

func (plainLAGWriter) ApplyLAGs(context.Context, []LAGState) (ApplyResult, error) {
	return ApplyResult{}, nil
}

func TestLAGFallbackGestureFollowsTheMethodSet(t *testing.T) {
	var with = GesturesOf((*fallbackWriter)(nil))
	var saw = map[Gesture]bool{}
	var g Gesture
	for _, g = range with {
		saw[g] = true
	}
	if !saw[GestureLAGs] || !saw[GestureLAGFallback] {
		t.Fatalf("a fallback writer must declare both lags and lag-fallback, got %v", with)
	}

	saw = map[Gesture]bool{}
	for _, g = range GesturesOf((*plainLAGWriter)(nil)) {
		saw[g] = true
	}
	if !saw[GestureLAGs] {
		t.Fatal("a plain LAG writer must still declare lags")
	}
	if saw[GestureLAGFallback] {
		t.Fatal("a plain LAG writer must NOT declare lag-fallback: the screen " +
			"would offer a control the connector ignores")
	}
}

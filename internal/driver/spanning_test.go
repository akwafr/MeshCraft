package driver

import "testing"

func TestNormalizeSTPMode(t *testing.T) {
	var cases = []struct {
		in, want string
	}{
		{"rstp-pvst", "rstp"},
		{"RSTP", "rstp"},
		{"mstp", "mstp"},
		{"none", "none"},
		{"off", "none"},
		{"stp", "stp"},
	}
	var i int
	for i = range cases {
		var got = NormalizeSTPMode(cases[i].in)
		if got != cases[i].want {
			t.Errorf("NormalizeSTPMode(%q)=%q want %q", cases[i].in, got, cases[i].want)
		}
	}
}

func TestResolveSTPMode(t *testing.T) {
	var mode string
	var disable, ok bool

	mode, disable, ok = ResolveSTPMode(
		[]string{"mstp", "rstp-pvst", "none"},
		[]string{"rstp"},
	)
	if !ok || disable || mode != "rstp" {
		t.Errorf("swos-like: mode=%q disable=%v ok=%v", mode, disable, ok)
	}

	// SwOS cannot disable via preference « none » — skip past it, then no match.
	mode, disable, ok = ResolveSTPMode(
		[]string{"mstp", "none"},
		[]string{"rstp"},
	)
	if ok {
		t.Errorf("swos must not honor none: mode=%q disable=%v ok=%v", mode, disable, ok)
	}

	mode, disable, ok = ResolveSTPMode(
		[]string{"mstp"},
		[]string{"rstp"},
	)
	if ok {
		t.Errorf("no match should fail: mode=%q disable=%v", mode, disable)
	}

	mode, disable, ok = ResolveSTPMode(
		[]string{"stp", "rstp-pvst"},
		[]string{"none", "stp", "rstp", "mstp"},
	)
	if !ok || disable || mode != "stp" {
		t.Errorf("first match stp: mode=%q disable=%v ok=%v", mode, disable, ok)
	}

	mode, disable, ok = ResolveSTPMode(
		[]string{"mstp", "none"},
		[]string{"none", "stp", "rstp", "mstp"},
	)
	if !ok || disable || mode != "mstp" {
		t.Errorf("routeros/instanton mstp: mode=%q disable=%v ok=%v", mode, disable, ok)
	}

	mode, disable, ok = ResolveSTPMode(
		[]string{"none"},
		[]string{"none", "stp", "rstp", "mstp"},
	)
	if !ok || !disable || mode != "" {
		t.Errorf("routeros/instanton none: mode=%q disable=%v ok=%v", mode, disable, ok)
	}
}

func TestSanitizeSTPPreference(t *testing.T) {
	var got = SanitizeSTPPreference([]string{"mstp", "rstp", "bogus", "mstp", "none", "rstp-pvst"})
	if len(got) != 4 || got[0] != "mstp" || got[1] != "rstp" || got[2] != "none" || got[3] != "rstp-pvst" {
		t.Errorf("got %#v", got)
	}
}

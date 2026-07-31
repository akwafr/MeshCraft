package store

import "testing"

func TestSlugifySite(t *testing.T) {
	var cases = []struct {
		in, want string
	}{
		{"Lab", "lab"},
		{"Lyon e-Sport", "lyon-e-sport"},
		{"  Foo   Bar  ", "foo-bar"},
		{"API", "api"},
	}
	var c = cases[0]
	for _, c = range cases {
		var got = slugifySite(c.in)
		if got != c.want {
			t.Errorf("slugifySite(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidSiteSlugRejectsReserved(t *testing.T) {
	if validSiteSlug("config") {
		t.Error("config must be reserved")
	}
	if !validSiteSlug("lab") {
		t.Error("lab must be valid")
	}
	if validSiteSlug("a") {
		t.Error("single char must be rejected")
	}
}

package mdpv1

import (
	"strings"
	"testing"
)

// TestUnknownIsNotZero — the whole point of the type.
//
// Level 0 is a REAL privilege on FASTPATH and on IOS. If "the device did not
// say" collapsed onto it, an audit reading the list would see a harmless
// level-0 account and a hidden administrator whose level the source never
// stated as the same thing. The EdgeSwitch connector got this right on
// 2026-08-10 and the fleet type inherits it; this test is the ratchet.
func TestUnknownIsNotZero(t *testing.T) {
	if PrivilegeUnknown == 0 {
		t.Fatal("PrivilegeUnknown must not be 0: 0 is a real level on several of " +
			"these platforms, and folding the two loses the distinction the audit needs")
	}
	if AuthorityFromLevel(0) != AuthorityLimited {
		t.Errorf("level 0 = %v, want limited: it is a stated level, not an absence",
			AuthorityFromLevel(0))
	}
	if AuthorityFromLevel(PrivilegeUnknown) != AuthorityUnknown {
		t.Errorf("an unstated privilege ranks nothing, got %v",
			AuthorityFromLevel(PrivilegeUnknown))
	}
	if PrivilegeWord(0) == PrivilegeWord(PrivilegeUnknown) {
		t.Error("\"level 0\" and \"not stated\" must not read alike in a transcript")
	}
}

// TestZeroAccountClaimsNothing: AuthorityUnknown is the zero value on purpose.
// A struct nobody filled in must not read as "limited" — that would be a
// connector silently vouching for an account it never looked at.
func TestZeroAccountClaimsNothing(t *testing.T) {
	var a Account
	if a.Authority != AuthorityUnknown {
		t.Errorf("the zero Authority is %v, want unknown", a.Authority)
	}
}

func TestAuthorityFromLevel(t *testing.T) {
	var cases = []struct {
		level int
		want  Authority
	}{
		{PrivilegeUnknown, AuthorityUnknown},
		{-7, AuthorityUnknown}, // no device produces this; it must not rank
		{0, AuthorityLimited},
		{1, AuthorityLimited},
		{14, AuthorityLimited},
		{15, AuthorityFull},
		{16, AuthorityFull},
	}
	var i int
	for i = range cases {
		var got = AuthorityFromLevel(cases[i].level)
		if got != cases[i].want {
			t.Errorf("level %d → %v, want %v", cases[i].level, got, cases[i].want)
		}
	}
}

// TestAccountAtLevelKeepsTheTwoFieldsInStep — the reason the constructor
// exists. Four connectors fill these in; one of them setting the number and
// forgetting the verdict would report "privilege 15, limited".
func TestAccountAtLevelKeepsTheTwoFieldsInStep(t *testing.T) {
	var full = AccountAtLevel("mcadmin", 15, "show running-config")
	if full.Privilege != 15 || full.Authority != AuthorityFull {
		t.Errorf("%+v", full)
	}
	if full.Source != "show running-config" {
		t.Errorf("the source must survive: %+v", full)
	}
	var quiet = AccountAtLevel("ubnt", PrivilegeUnknown, "show users accounts detail")
	if quiet.Authority != AuthorityUnknown {
		t.Errorf("%+v: an account whose level was not stated ranks nothing", quiet)
	}
}

func TestHasAccountIgnoresCase(t *testing.T) {
	var have = []Account{AccountAtLevel("Admin", 15, "x")}
	if !HasAccount(have, "admin") {
		t.Error("devices differ on the case they echo back, and none of them lets " +
			"two accounts differ only by it: the lookup must fold case")
	}
	if HasAccount(have, "mcadmin") {
		t.Error("false positive")
	}
	if HasAccount(nil, "admin") {
		t.Error("no list names nobody")
	}
}

func TestAccountNames(t *testing.T) {
	if AccountNames(nil) != nil {
		t.Error("nil for nil")
	}
	var got = AccountNames([]Account{
		AccountAtLevel("ubnt", 15, "x"),
		AccountAtLevel("admin", 15, "x"),
	})
	if len(got) != 2 || got[0] != "ubnt" || got[1] != "admin" {
		t.Errorf("names = %v — order is the device's own", got)
	}
}

// TestDescribeSpeaksAllThreeVocabularies: a transcript line must be readable
// whether the platform counts, names, or says nothing.
func TestDescribeSpeaksAllThreeVocabularies(t *testing.T) {
	var numbered = AccountAtLevel("mcadmin", 15, "show running-config").Describe()
	if !strings.Contains(numbered, "15") || !strings.Contains(numbered, "FULL ADMINISTRATOR") {
		t.Errorf("numbered: %q", numbered)
	}
	var named = Account{Name: "mcadmin", Privilege: PrivilegeUnknown,
		Role: "full", Authority: AuthorityFull}
	if !strings.Contains(named.Describe(), "full administrator") || !strings.Contains(named.Describe(), "(full)") {
		t.Errorf("named: %q — the group is what RouterOS says, and it must show", named.Describe())
	}
	var silent = Account{Name: "admin", Privilege: PrivilegeUnknown}
	if !strings.Contains(silent.Describe(), "not stated") {
		t.Errorf("silent: %q — \"the device did not say\" must say so", silent.Describe())
	}
}

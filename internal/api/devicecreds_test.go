package api

import (
	"strings"
	"testing"
)

// countPerAccount: how many tries are planned on each account.
func countPerAccount(t *testing.T, names []string, operator string, gens []genSecret) map[string]int {
	t.Helper()
	var out = map[string]int{}
	var c = buildCandidates(names, operator, gens)
	var i int
	for i = range c {
		out[strings.ToLower(c[i].Username)]++
	}
	return out
}

// TestCandidatesRespectPerAccountCap: "two tries per account, immediate stop
// beyond" (docs/en/03-Security/Service identity and rotation.md §5).
//
// The cap protects against account lockout. On a range without SSH or serial
// port, a locked account is fixed by moving on: that is the spec rule that is
// most costly to break.
func TestCandidatesRespectPerAccountCap(t *testing.T) {
	// Realistic worst case: three known generations, two account names,
	// and an operator-supplied password.
	var gens = []genSecret{
		{Why: "generation observed on this device", Secret: "g3"},
		{Why: "retired generation", Secret: "g2"},
		{Why: "retired generation", Secret: "g1"},
	}
	var perAccount = countPerAccount(t, []string{"mcadmin", "admin"}, "entered", gens)

	var name string
	var n int
	for name, n = range perAccount {
		if n > triesPerAccount {
			t.Errorf("account %q: %d tries, maximum is %d", name, n, triesPerAccount)
		}
	}
	if perAccount["mcadmin"] == 0 {
		t.Error("the targeted account must be tried")
	}
}

// TestCandidatesKeepFactoryAccount: a never-provisioned device has no password.
// If generations consumed the factory account's two tries, it would become
// unadoptable — when hitting without a secret is enough.
func TestCandidatesKeepFactoryAccount(t *testing.T) {
	var gens = []genSecret{
		{Why: "current generation", Secret: "g3"},
		{Why: "retired generation", Secret: "g2"},
		{Why: "retired generation", Secret: "g1"},
	}
	var c = buildCandidates([]string{"mcadmin", "admin"}, "", gens)
	if len(c) == 0 {
		t.Fatal("no candidates")
	}
	// The factory account is LAST: at the top, it would fail the entire fleet
	// already under management.
	var last = c[len(c)-1]
	if last.Username != factoryAccount || last.Secret != "" {
		t.Errorf("last try must be factory account without password, got %+v", last)
	}
}

// TestAccountNamesOrderedAndDeduped: OBSERVED name first — most likely to
// respond —, then wanted name, then factory.
func TestAccountNamesOrderedAndDeduped(t *testing.T) {
	var cas = []struct {
		name      string
		installed string
		wanted    string
		expected  []string
	}{
		{"renamed device", "mcadmin", "mcadmin", []string{"mcadmin", "admin"}},
		{"rename in progress", "admin", "mcadmin", []string{"admin", "mcadmin"}},
		{"never provisioned", "", "mcadmin", []string{"mcadmin", "admin"}},
		{"different case", "Admin", "admin", []string{"Admin"}},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.name, func(t *testing.T) {
			var got = accountNames(c.installed, c.wanted)
			if strings.Join(got, ",") != strings.Join(c.expected, ",") {
				t.Errorf("accountNames(%q,%q) = %v, expected %v",
					c.installed, c.wanted, got, c.expected)
			}
		})
	}
}

// The discovery-origin pair (the secret the serial soft-reset actually typed on
// the console) must SURVIVE the try budget on a freshly adopted device: placed
// first among generations, bound to its account. The 2026-08-02 refusal came
// from four guessed candidates while the one observed pair was never tried.
func TestDiscoveryOriginPairSurvivesTheBudget(t *testing.T) {
	var gens = []genSecret{
		{Why: "discovery credential that opened the device (\"lab\")",
			Secret: "LabSecret1", Username: "admin"},
		{Why: "generation stepped back: device may not be updated yet", Secret: "gen2"},
		{Why: "generation stepped back: device may not be updated yet", Secret: "gen1"},
	}
	var c = buildCandidates([]string{"mcadmin", "admin"}, "", gens)

	if len(c) < 2 {
		t.Fatalf("candidates=%v", c)
	}
	if c[0].Username != "admin" || c[0].Secret != "LabSecret1" {
		t.Fatalf("the observed pair must be tried FIRST, got %+v", c[0])
	}
	// Factory reserve still holds: last candidate is admin with empty secret.
	var last = c[len(c)-1]
	if last.Username != "admin" || last.Secret != "" {
		t.Errorf("factory account must stay last: %+v", last)
	}
	// The budget still caps admin at two tries total (origin + factory reserve):
	// no stepped-back generation may squeeze in on that account.
	var adminTries = 0
	var cand = c[0]
	for _, cand = range c {
		if strings.EqualFold(cand.Username, "admin") {
			adminTries++
		}
	}
	if adminTries > 2 {
		t.Errorf("admin tried %d times, budget is 2", adminTries)
	}
}

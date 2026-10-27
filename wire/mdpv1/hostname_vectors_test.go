package mdpv1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// THE GO HALF OF THE CROSS-LANGUAGE PARITY TEST.
//
// # What was wrong with the arrangement this replaces
//
// The hostname rule has TWO implementations — internal/driver/hostname.go and
// web/src/pages/add/hostname.ts — because the browser scripts are what actually
// reach hardware over Web Serial while the healer runs in the controller. Both
// files say, in prose, that one mirrors the other. Until this test existed that
// was the ONLY thing saying it: hostname_test.go and hostname.test.ts happened to
// carry similar cases, written by hand, and NOTHING failed when one side was
// edited and the other was not. Prose does not fail a build.
//
// That is exactly the drift that produced the ten spellings collapsed on
// 2026-08-09/10, so the unification would have come undone the first time somebody
// "fixed" one side. The consequence is written out in both files: a switch
// provisioned from a console (TS) and the same switch reconciled by the healer
// (Go) would be named two different things, and the healer would then chase a
// drift it can never resolve.
//
// # The shared table, and why it lives in web/
//
// One JSON file of input → expected-output vectors, read by THIS test and by
// web/src/pages/add/hostname_vectors.test.ts. Neither side asserts against the
// other's prose any more; both assert against the same table, so a change that
// moves one implementation fails on the side that did NOT move.
//
// It lives under web/ because of the container mounts, which are the real
// constraint here (Makefile): `make test-web` mounts ONLY ./web, so the TS suite
// can read nothing outside it, while `make test-go` mounts the whole repo at
// /src. web/ is therefore the only directory both suites can see, and the reach
// across has to be Go's.
//
// # It must never SKIP
//
// A parity test that skips when it cannot find its table reports success while
// asserting nothing, which is worse than not existing: it occupies the slot where
// somebody would otherwise have noticed there is no guard. Every failure mode
// below — missing file, unparseable file, empty table, missing TS consumer — is a
// t.Fatalf.

// hostnameVectorsPath is the shared table, relative to the repository root.
const hostnameVectorsPath = "web/src/pages/add/hostname_vectors.json"

// hostnameVectorsTSSuite is the OTHER consumer. Asserted to exist and to read the
// table, because a shared table with one reader left is just a Go fixture with a
// misleading home — and the deletion would be invisible from this side.
const hostnameVectorsTSSuite = "web/src/pages/add/hostname_vectors.test.ts"

// hostnameVector is one line of the shared table. Field names are the JSON's.
type hostnameVector struct {
	Why             string   `json:"why"`
	In              string   `json:"in"`
	Hostname        string   `json:"hostname"`
	RefusalContains []string `json:"refusal_contains"`
}

type hostnameVectorTable struct {
	Readme  []string         `json:"_readme"`
	Vectors []hostnameVector `json:"vectors"`
}

// repoRelFile resolves a repository-root-relative path by walking UP from the
// test's working directory until the file appears.
//
// `go test` runs each package with its own source directory as the working
// directory, so this package sees /src/internal/driver and the table is two
// levels up. The walk rather than a literal `../../` is what keeps the test
// working if this package is ever moved or vendored a level deeper; the depth
// bound is there so a genuinely missing file fails fast instead of walking to /.
func repoRelFile(t *testing.T, rel string) string {
	t.Helper()
	var dir, err = os.Getwd()
	if err != nil {
		t.Fatalf("cannot determine the working directory, so the shared hostname "+
			"vector table cannot be located: %v", err)
	}
	var start = dir
	var depth int
	for depth = 0; depth < 12; depth++ {
		var candidate = filepath.Join(dir, filepath.FromSlash(rel))
		var _, statErr = os.Stat(candidate)
		if statErr == nil {
			return candidate
		}
		var parent = filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// NOT a skip. See the header: a parity test that quietly stands down reports
	// green while comparing nothing.
	t.Fatalf("%s was not found walking up from %s. This is the table the Go and "+
		"TypeScript hostname normalizers are BOTH compared against; without it the "+
		"two implementations are unguarded, so this test fails rather than skips.",
		rel, start)
	return ""
}

// loadHostnameVectors reads and validates the shared table itself, before any
// vector is run.
//
// The table is validated as a table — non-empty, no duplicate inputs, exactly one
// of hostname/refusal expected per line — because a malformed table is the other
// way this test could pass while asserting nothing (an empty `vectors` array runs
// zero comparisons and reports success).
func loadHostnameVectors(t *testing.T) []hostnameVector {
	t.Helper()
	var path = repoRelFile(t, hostnameVectorsPath)
	var raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("the shared hostname vector table %s could not be read: %v", path, err)
	}
	var table hostnameVectorTable
	err = json.Unmarshal(raw, &table)
	if err != nil {
		t.Fatalf("the shared hostname vector table %s is not parseable JSON: %v",
			path, err)
	}
	if len(table.Vectors) < 40 {
		t.Fatalf("the shared hostname vector table carries %d vectors. It covered 54 "+
			"when it was written and it is the ONLY thing comparing the Go and "+
			"TypeScript normalizers: shrinking it silently narrows the guard.",
			len(table.Vectors))
	}
	var seen = map[string]bool{}
	var v hostnameVector
	var i int
	for i, v = range table.Vectors {
		if seen[v.In] {
			t.Errorf("vector %d repeats the input %q — two lines of the table "+
				"disagreeing about one input is a table nobody can fix", i, v.In)
		}
		seen[v.In] = true
		if v.Why == "" {
			t.Errorf("vector %d (%q) says nothing about what it defends; a vector "+
				"whose reason is unwritten gets deleted the first time it is "+
				"inconvenient", i, v.In)
		}
		if v.Hostname != "" && len(v.RefusalContains) > 0 {
			t.Errorf("vector %d (%q) expects BOTH a hostname and a refusal; the "+
				"contract is that exactly one of them exists", i, v.In)
		}
		if v.Hostname == "" && len(v.RefusalContains) == 0 {
			t.Errorf("vector %d (%q) expects a refusal but pins nothing about it, so "+
				"any refusal at all would satisfy it — including the wrong diagnosis",
				i, v.In)
		}
	}
	return table.Vectors
}

// TestHostnameNormalizerMatchesSharedVectors runs the shared table against the Go
// implementation. web/src/pages/add/hostname_vectors.test.ts runs the same table
// against the TypeScript one.
//
// REFUSALS ARE MATCHED ON MARKERS, not on whole sentences, and the table's header
// says why: the Go messages here are the operator-facing authority and are pinned
// word for word in hostname_test.go. Pinning them a second time in JSON would make
// a third place to update when a sentence is reworded — and a third place to
// forget is how this file's problem started. What the markers pin is the
// DIAGNOSIS: "does not shorten it" is the ceiling refusal and "may only carry
// letters" is the alphabet refusal, so a normalizer that rejects the right name
// for the wrong reason still fails.
func TestHostnameNormalizerMatchesSharedVectors(t *testing.T) {
	var vectors = loadHostnameVectors(t)
	var v hostnameVector
	for _, v = range vectors {
		var vec = v
		t.Run(vec.In, func(t *testing.T) {
			var got, refusal = HostnameFromDeviceName(vec.In)

			// The invariant every connector relies on now that none of them has a
			// sanitizer of its own: exactly one of the two is non-empty.
			if got != "" && refusal != "" {
				t.Errorf("%q returned both the name %q and the reason %q",
					vec.In, got, refusal)
			}
			if got == "" && refusal == "" {
				t.Errorf("%q returned neither a name nor a reason", vec.In)
			}
			if got != "" && !ValidHostname(got) {
				t.Errorf("%q produced %q, which ValidHostname itself rejects — a "+
					"connector would concatenate that straight into a command line",
					vec.In, got)
			}

			if got != vec.Hostname {
				t.Errorf("shared vector disagrees with the Go normalizer.\n"+
					"  input:    %q\n  expected: %q\n  got:      %q\n  reason "+
					"given: %s\n  the vector defends: %s\n"+
					"If TypeScript agrees with the table and Go does not, Go moved.",
					vec.In, vec.Hostname, got, refusal, vec.Why)
			}
			var marker string
			for _, marker = range vec.RefusalContains {
				if !strings.Contains(refusal, marker) {
					t.Errorf("the refusal for %q does not carry the marker %q.\n"+
						"  refusal: %s\n  the vector defends: %s",
						vec.In, marker, refusal, vec.Why)
				}
			}
		})
	}
}

// TestSharedVectorsCoverTheOctetTrap pins the one vector that cannot be
// reconstructed by intuition, so that a table trimmed for tidiness cannot lose it.
//
// A name that is UNDER 63 characters but OVER 63 octets is where Go's len() and a
// JavaScript .length would silently part company: Go refuses it at the ceiling
// while a .length mirror sails past and hands back the alphabet refusal instead.
// The visible symptom is only WHICH refusal comes out — but the disagreement
// underneath is about how long a name may be, and on an all-ASCII name that same
// disagreement renames a device on one path and not the other.
func TestSharedVectorsCoverTheOctetTrap(t *testing.T) {
	var vectors = loadHostnameVectors(t)
	var found = false
	var v hostnameVector
	for _, v = range vectors {
		if len(v.In) > 63 && utf8.RuneCountInString(v.In) <= 63 {
			found = true
			if v.Hostname != "" {
				t.Errorf("the multi-byte over-the-ceiling vector %q expects the name "+
					"%q; over 63 OCTETS must refuse", v.In, v.Hostname)
			}
		}
	}
	if !found {
		t.Fatal("the shared table no longer carries a name that is under 63 " +
			"CHARACTERS but over 63 OCTETS. That is the single vector that " +
			"distinguishes an octet-counting mirror from a .length one, and losing " +
			"it is how the two normalizers stop agreeing about the ceiling without " +
			"any test noticing.")
	}
}

// TestSharedVectorsAreStillReadByTypeScript: this table only guards anything
// while BOTH suites read it. If the TypeScript half is deleted or renamed, the Go
// side keeps passing and nothing anywhere reports that the comparison stopped
// happening — so the Go side checks that its counterpart is still there.
//
// The TS suite cannot return the favour: `make test-web` mounts only ./web, so it
// cannot see internal/driver at all. The asymmetry is the mount's, not a choice.
func TestSharedVectorsAreStillReadByTypeScript(t *testing.T) {
	var path = repoRelFile(t, hostnameVectorsTSSuite)
	var raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s could not be read: %v", hostnameVectorsTSSuite, err)
	}
	var src = string(raw)
	if !strings.Contains(src, filepath.Base(hostnameVectorsPath)) {
		t.Fatalf("%s no longer loads %s. With one reader left the table stops being "+
			"a parity guard and becomes a Go fixture with a misleading home.",
			hostnameVectorsTSSuite, filepath.Base(hostnameVectorsPath))
	}
	if !strings.Contains(src, "hostnameFromDeviceName") {
		t.Fatalf("%s no longer calls hostnameFromDeviceName, so whatever it is "+
			"comparing the table against is not the TypeScript normalizer",
			hostnameVectorsTSSuite)
	}
}

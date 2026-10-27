package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// THE LLDP ARBITRATION HAS EXACTLY ONE CLOCK, and this is what enforces it.
//
// `api.markRefutedObservations` and `api.refuteByFreshSilence` decide whether a
// cable still exists by comparing two STORED DATES to each other — an observation's
// `last_seen` against the far device's `seen_at`. Neither compares a date to
// `time.Now()`, deliberately, so the whole verdict rests on both dates having been
// stamped by the SAME clock.
//
// They are. `ReplaceLLDP` hardcodes `now()` on the PostgreSQL server for all three
// time columns, so a caller that fills `LLDPSelf.SeenAt` or `LLDPNeighbor.LastSeen`
// on the model has the value discarded — there is no parameter to bind a client
// clock to. Migration 0028 states the rule on the schema and gives the number that
// motivates it: proxy clock drift measured at eleven seconds on the lab.
//
// # Why a source scan and not a behaviour test
//
// The property was written down in three comments and enforced by nothing. What
// would break it is not a wrong value at run time, it is a future WRITE PATH — a
// second poller, a bulk import, a "restore the last known neighbourhood" gesture —
// binding a client timestamp to one of these columns. That breaks the arbitration
// SILENTLY: no error, no failing assertion, links simply start being retired or
// held up according to whichever device's clock ran ahead. A behaviour test on
// ReplaceLLDP would not see it either, since the new path would be a different
// function.
//
// So the package is PARSED, in the same spirit as scripts/shortdecl, and every SQL
// literal that WRITES `lldp_self` or `lldp_neighbors` has to stamp each time column
// it touches with `now()`. This package has no database in its tests (cf.
// devices_test.go, identity_test.go) and this check needs none.
//
// A write shape the scanner cannot read is a FAILURE, not a pass. A guard that
// quietly stops looking is worse than no guard, because it goes on being cited.

// lldpTimeColumns: the columns the arbitration compares. Named once so a fourth
// one added to the schema is added in one place.
var lldpTimeColumns = []string{"seen_at", "first_seen", "last_seen"}

// lldpTables: the two tables whose rows carry those dates.
var lldpTables = []string{"lldp_self", "lldp_neighbors"}

// flattenSQL collapses a multi-line backtick literal to one lowercase line, so the
// checks below can be written against token order instead of formatting.
func flattenSQL(raw string) string {
	return strings.ToLower(strings.Join(strings.Fields(raw), " "))
}

// parenGroup returns the contents of the parenthesis group that opens at or after
// `from`, and the index just past its close. ok=false when there is none, or when
// it is unbalanced.
func parenGroup(s string, from int) (string, int, bool) {
	var open = strings.Index(s[from:], "(")
	if open < 0 {
		return "", 0, false
	}
	open += from
	var depth int
	var i int
	for i = open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[open+1 : i], i + 1, true
			}
		}
	}
	return "", 0, false
}

// splitTopLevel splits on the commas that are NOT inside parentheses, so `now()`
// survives as a single item in a values list.
func splitTopLevel(s string) []string {
	var out []string
	var depth int
	var start int
	var i int
	for i = 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

// sqlWritesLLDP reports whether this statement is an INSERT or UPDATE naming one of
// the two tables. A `select` or a `delete` mentioning them is not a clock decision.
func sqlWritesLLDP(sql string) bool {
	if !strings.HasPrefix(sql, "insert ") && !strings.HasPrefix(sql, "update ") {
		return false
	}
	var table string
	for _, table = range lldpTables {
		if strings.Contains(sql, table) {
			return true
		}
	}
	return false
}

// checkAssignedTimeColumns walks every `<time column> = <value>` in a statement —
// an `update ... set`, or the `on conflict do update set` tail of an upsert — and
// requires the value to be `now()`.
func checkAssignedTimeColumns(t *testing.T, where, sql string) {
	t.Helper()
	var col string
	for _, col = range lldpTimeColumns {
		var needle = col + " ="
		var rest = sql
		for {
			var at = strings.Index(rest, needle)
			if at < 0 {
				break
			}
			rest = rest[at+len(needle):]
			var value = splitTopLevel(rest)[0]
			if value != "now()" {
				t.Errorf("%s: `%s` is assigned %q. It must be stamped `now()` on the "+
					"PostgreSQL server: api.markRefutedObservations and "+
					"api.refuteByFreshSilence compare this column against another row's "+
					"date, and a value carried in from a client puts a second clock into "+
					"that comparison", where, col, value)
			}
		}
	}
}

// checkInsertedTimeColumns zips an insert's column list against its values list and
// requires every time column to receive `now()` positionally. This is the half a
// `<col> =` scan cannot see, because in `values (...)` the column is never named
// next to its value.
func checkInsertedTimeColumns(t *testing.T, where, sql string) {
	t.Helper()
	var cols, afterCols, okCols = parenGroup(sql, 0)
	if !okCols {
		t.Fatalf("%s: an insert into an LLDP table with no column list. The scanner "+
			"cannot judge it, and an unreadable write must not pass: %s", where, sql)
	}
	var at = strings.Index(sql[afterCols:], "values")
	if at < 0 {
		t.Fatalf("%s: an insert into an LLDP table with no `values` clause. The scanner "+
			"cannot judge it, and an unreadable write must not pass: %s", where, sql)
	}
	var vals, _, okVals = parenGroup(sql, afterCols+at)
	if !okVals {
		t.Fatalf("%s: unbalanced `values` list: %s", where, sql)
	}
	var names = splitTopLevel(cols)
	var values = splitTopLevel(vals)
	if len(names) != len(values) {
		t.Fatalf("%s: %d columns for %d values — the scanner is reading this statement "+
			"wrong and must not report it clean: %s", where, len(names), len(values), sql)
	}
	var i int
	for i = range names {
		var col string
		for _, col = range lldpTimeColumns {
			if names[i] != col {
				continue
			}
			if values[i] != "now()" {
				t.Errorf("%s: `%s` is inserted as %q. It must be stamped `now()` on the "+
					"PostgreSQL server, or the freshness arbitration in internal/api is "+
					"comparing two clocks", where, col, values[i])
			}
		}
	}
}

// storePackageSQL returns every string literal in the package's NON-TEST sources,
// with the file and line it came from.
//
// Test files are excluded on purpose: this very file quotes column names and SQL
// fragments in its messages, and a guard that trips over its own diagnostics
// teaches nothing.
func storePackageSQL(t *testing.T) map[string]string {
	t.Helper()
	var entries, err = os.ReadDir(".")
	if err != nil {
		t.Fatalf("cannot read the package directory: %v", err)
	}
	var out = map[string]string{}
	var fset = token.NewFileSet()
	var e os.DirEntry
	for _, e = range entries {
		var name = e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		var file *ast.File
		file, err = parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("cannot parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			var lit, isLit = n.(*ast.BasicLit)
			if !isLit || lit.Kind != token.STRING {
				return true
			}
			var text, uErr = strconv.Unquote(lit.Value)
			if uErr != nil {
				return true
			}
			out[fset.Position(lit.Pos()).String()] = text
			return true
		})
	}
	return out
}

// TestLLDPTimestampsAreStampedByTheServer is the guard the arbitration's "one
// clock" property rests on. Cited by name from api.markRefutedObservations.
func TestLLDPTimestampsAreStampedByTheServer(t *testing.T) {
	var literals = storePackageSQL(t)
	var checked int
	var where string
	for where = range literals {
		var sql = flattenSQL(literals[where])
		if !sqlWritesLLDP(sql) {
			continue
		}
		checked++
		checkAssignedTimeColumns(t, where, sql)
		if strings.HasPrefix(sql, "insert ") {
			checkInsertedTimeColumns(t, where, sql)
		} else if !strings.Contains(sql, " set ") {
			t.Errorf("%s: an update of an LLDP table with no `set` clause. The scanner "+
				"cannot judge it, and an unreadable write must not pass: %s", where, sql)
		}
	}
	// ReplaceLLDP writes both tables, so there are two. Zero means the scan stopped
	// working — a package reorganised, a query moved into a helper the parser no
	// longer sees — and a guard that finds nothing must say so rather than pass.
	if checked < 2 {
		t.Fatalf("found %d LLDP write statements in this package, expected at least 2 "+
			"(the lldp_self upsert and the lldp_neighbors upsert). The scan is no longer "+
			"seeing what it was written to watch", checked)
	}
}

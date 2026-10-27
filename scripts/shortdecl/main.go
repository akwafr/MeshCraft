// Command shortdecl counts the short declaration operator `:=` in Go source and
// fails when there are more of them than a recorded baseline allows.
//
// CLAUDE.md rule #1 forbids `:=` everywhere in this project, tests included, but
// `go vet` has nothing to say about it and the rule drifted for months. This is
// the mechanical guard: a ratchet, not a sweep. It does not fix anything, it
// only makes the debt unable to grow.
//
// # Why not grep
//
// A `grep ':='` is wrong here, and not marginally. This repository documents the
// rule in prose and quotes the forbidden form in comments, in error messages and
// in test fixtures; every one of those would be counted, the baseline would be
// noise, and lowering it would stop meaning anything.
//
// So the file is PARSED with go/parser and the count is taken from the syntax
// tree. A `:=` inside a // comment, a /* */ comment, an interpreted string, a
// raw backtick string or a rune literal is not a node and can never be counted.
// There is no stripping heuristic to get subtly wrong: correctness here is the
// parser's, not ours.
//
// # The one exemption: a type switch guard
//
// `switch v := x.(type)` is NOT a short variable declaration. It is a distinct
// grammar production (ast.TypeSwitchStmt.Assign), and unlike every other `:=` in
// the language it has NO `var` form — the binding is what gives each case clause
// its narrowed type. The only way to "obey" the rule is to switch on the type
// alone and repeat the assertion in every branch, which on the nine-branch
// switch in internal/gnmipoll/transport.go turns `v.DecimalVal` into
// `raw.(*gnmipb.TypedValue_DecimalVal).DecimalVal` nine times over.
//
// That was tried, on 2026-08-08, and reverted. It is more code, it is harder to
// read, and it moves a check the compiler was doing for us into an assertion
// that panics at run time if the branch and the assertion ever disagree. The
// rule exists to make declarations explicit and greppable; applying it here
// costs safety and buys nothing.
//
// So the guard is exempt, and this is the only exemption. A `:=` in a type
// switch's INITIALISER (`switch a := f(); v := x.(type)`) is an ordinary short
// declaration and is still counted.
//
// Usage:
//
//	go run ./scripts/shortdecl -baseline 129 ./internal ./cmd ./tools
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// site is one offending `:=`, reported as file:line so a developer who trips the
// ratchet sees what they added rather than a number that went up.
type site struct {
	File string
	Line int
	Col  int
}

func main() {
	var baseline int
	flag.IntVar(&baseline, "baseline", 0, "maximum number of `:=` tolerated")
	flag.Parse()

	var roots = flag.Args()
	if len(roots) == 0 {
		fmt.Fprintln(os.Stderr, "shortdecl: no directory given")
		os.Exit(2)
	}

	var sites []site
	var scanErrs []string
	var root string
	for _, root = range roots {
		var info, statErr = os.Stat(root)
		if statErr != nil || !info.IsDir() {
			// A missing scan root is not an excuse to pass silently: the target
			// would quietly stop covering a tree that got renamed.
			fmt.Fprintf(os.Stderr, "shortdecl: cannot scan %s: %v\n", root, statErr)
			os.Exit(2)
		}
		var walkErr = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				var name = d.Name()
				// vendor/ is other people's code, and dot-directories are not ours
				// to police. testdata IS scanned: a fixture is still Go we wrote.
				if name == "vendor" || (strings.HasPrefix(name, ".") && len(name) > 1) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			var found, fileErrs = scanFile(path)
			sites = append(sites, found...)
			scanErrs = append(scanErrs, fileErrs...)
			return nil
		})
		if walkErr != nil {
			fmt.Fprintf(os.Stderr, "shortdecl: walking %s: %v\n", root, walkErr)
			os.Exit(2)
		}
	}

	// A file the lexer cannot read would be counted as zero, which is an
	// undercount pretending to be progress. Refuse instead.
	if len(scanErrs) > 0 {
		var e string
		for _, e = range scanErrs {
			fmt.Fprintln(os.Stderr, "shortdecl: "+e)
		}
		fmt.Fprintln(os.Stderr, "shortdecl: source could not be tokenized; count is not trustworthy")
		os.Exit(2)
	}

	sort.Slice(sites, func(i, j int) bool {
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}
		return sites[i].Line < sites[j].Line
	})

	var n = len(sites)
	if n > baseline {
		fmt.Fprintf(os.Stderr,
			"\nshortdecl: %d short declarations `:=`, baseline is %d — %d too many.\n"+
				"CLAUDE.md rule #1: declare with `var`, never `:=`.\n\n", n, baseline, n-baseline)
		var s site
		for _, s = range sites {
			fmt.Fprintf(os.Stderr, "  %s:%d:%d\n", s.File, s.Line, s.Col)
		}
		fmt.Fprintf(os.Stderr,
			"\nRaising LINT_BASELINE is not the fix. Rewrite the new ones with `var`.\n")
		os.Exit(1)
	}

	fmt.Printf("shortdecl: %d `:=` (baseline %d)\n", n, baseline)
	if n < baseline {
		fmt.Printf("shortdecl: debt went down — lower LINT_BASELINE to %d in the Makefile.\n", n)
	}
	os.Exit(0)
}

// scanFile parses one file and returns every short declaration in it.
//
// Counted: ast.AssignStmt with Tok == token.DEFINE (`a := f()`), and
// ast.RangeStmt with Tok == token.DEFINE (`for i := range xs`). Both have a
// straightforward `var` form and both are what the rule is about.
//
// Not counted: the guard of a type switch. See the package comment — it is a
// different production with no `var` form, and it is the only exemption.
// A `:=` in the type switch's Init is an ordinary declaration and IS counted,
// which falls out for free: only the Assign field is exempted, not the node.
func scanFile(path string) ([]site, []string) {
	var src, err = os.ReadFile(path)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s: %v", path, err)}
	}

	var fset = token.NewFileSet()
	var file *ast.File
	file, err = parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		// A file that will not parse would count as zero, which is an undercount
		// wearing the face of progress. Report it and let the caller refuse.
		return nil, []string{fmt.Sprintf("%s: %v", path, err)}
	}

	// The guards to skip, collected first so the walk below can recognise them by
	// identity rather than by trying to infer context mid-traversal.
	var exempt = map[ast.Node]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		var ts, ok = n.(*ast.TypeSwitchStmt)
		if ok && ts.Assign != nil {
			exempt[ts.Assign] = true
		}
		return true
	})

	var out []site
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil || exempt[n] {
			return true
		}
		var pos token.Pos = token.NoPos
		var as, isAssign = n.(*ast.AssignStmt)
		if isAssign && as.Tok == token.DEFINE {
			pos = as.TokPos
		}
		var rs, isRange = n.(*ast.RangeStmt)
		if isRange && rs.Tok == token.DEFINE {
			pos = rs.TokPos
		}
		if pos != token.NoPos {
			var p = fset.Position(pos)
			out = append(out, site{File: p.Filename, Line: p.Line, Col: p.Column})
		}
		return true
	})
	return out, nil
}

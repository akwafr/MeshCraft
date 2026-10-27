// Fixture for scripts/shortdecl. It is under testdata/, so the go tool ignores
// it and it is never built with the rest of the module.
//
// It exists to PROVE the counter parses rather than greps. The correct answer
// is 5: the three short declarations in realOnes(), the `for … := range` in
// ranging(), and the `n := 1` initialiser in typeSwitching().
//
// Everything else is either a `:=` living inside a comment, a string or a rune
// pair — where it is not an operator at all — or the guard of a type switch,
// which is a different production with no `var` form and is exempt on purpose
// (see the shortdecl package comment). A naive `grep -c ':='` counts them all.
//
// This line is a // comment holding one: x := 1
/* And a block comment holding two:
   y := 2
   z := 3 */
package tricky

// realOnes holds the ONLY three := that a correct counter reports.
func realOnes() int {
	a := 1
	b := 2
	c := a + b
	return c
}

func decoys() []string {
	// Interpreted strings.
	var s1 = "forbidden: v := f()"
	var s2 = "escaped \" quote then v := f() after it"

	// Raw backtick string, the case a line-oriented stripper gets wrong: the
	// := below is inside a multi-line literal, not code.
	var s3 = `
		u, err := store.GetUser(ctx, id)
		for i := range items { }
	`

	// A rune pair spelling the operator one character at a time.
	var colon = ':'
	var equal = '='
	var s4 = string(colon) + string(equal)

	// A `:=` inside a struct tag, which is also a string literal.
	type tagged struct {
		F int `note:"written x := 1 on purpose"`
	}
	var t tagged
	_ = t

	return []string{s1, s2, s3, s4}
}

// ranging pins that a range clause IS counted: it has a plain `var` form.
func ranging(items []int) int {
	var total int
	for i := range items {
		total += items[i]
	}
	return total
}

// typeSwitching pins the ONE exemption. The guard binds `v` so each case sees a
// narrowed type; there is no `var` spelling of that, so it is not counted.
//
// The `n := 1` initialiser beside it is an ordinary short declaration and IS
// counted — proving the exemption is scoped to the guard, not to the whole
// statement.
func typeSwitching(x any) string {
	switch n := 1; v := x.(type) {
	case int:
		return string(rune(v + n))
	case string:
		return v
	}
	return ""
}

package procurve

// Reading this platform's "show" tables, which are FIXED-WIDTH and cannot be
// split on whitespace.
//
// # Why a ruler parser and not strings.Fields
//
// Every status view on this range prints a ruler line under its header, and the
// columns are positional. Splitting on whitespace works until a cell is EMPTY,
// and then it silently shifts every later field by one. That is not theoretical
// — it is the shape of the reference unit's own faceplate (CAPTURED
// 2026-08-03, 2610-48 R.11.121):
//
//	  Port  Type      | Alert     Enabled Status Mode       Mode  Ctrl  Limit
//	  ----- --------- + --------- ------- ------ ---------- ----- ----- ------
//	  1     10/100TX  | No        No      Down   100FDx     MDIX  off   0
//	  51              | No        Yes     Down                    off   0
//
// Port 51 is an EMPTY SFP cage: its Type and Mode cells are blank. Split on
// whitespace and "off" lands in the MDI column, "0" in flow control, and the
// row reads as a port whose link state came from the wrong cell. Read by ruler
// and the blanks stay blank.
//
// The ruler is also the SCHEMA: rather than hard-code offsets that a firmware
// revision could move, each parser finds its ruler line and derives the spans
// from it. A column that moves moves in both places at once.

import "strings"

// span is one column, as [start,end) byte offsets into a row.
type span struct{ from, to int }

// parseRuler turns a "----- --------- + ---------" line into column spans.
//
// Runs of '-' are columns; everything else — spaces and the '+' that marks
// where the data rows carry a '|' — is a gap. A line with fewer than two
// columns is not a ruler: returning nil lets the caller keep scanning rather
// than lock onto a horizontal rule drawn for decoration.
func parseRuler(line string) []span {
	var out []span
	var i int
	for i = 0; i < len(line); i++ {
		if line[i] != '-' {
			continue
		}
		var start = i
		for i < len(line) && line[i] == '-' {
			i++
		}
		out = append(out, span{from: start, to: i})
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// cell reads one column out of a row, trimmed.
//
// A row SHORTER than the ruler is normal: this CLI right-trims its lines, so
// the last cells of a row whose tail is blank simply are not there. That is an
// empty cell, not a malformed row.
func cell(row string, s span) string {
	if s.from >= len(row) {
		return ""
	}
	var to = s.to
	if to > len(row) {
		to = len(row)
	}
	return strings.TrimSpace(row[s.from:to])
}

// tableRows finds the table whose ruler follows a header containing every word
// in `headerWords`, and returns its data rows already split into cells.
//
// The header match is what keeps a parser on ITS table: "show spanning-tree"
// prints a bridge summary and a port table in one output, and "show ip" a
// gateway block and a VLAN table. Locking onto "the first ruler" would read
// whichever happened to come first.
//
// Scanning stops at the first line that is blank or that has NOTHING in the
// first column — this range terminates a table with a lone space.
func tableRows(text string, headerWords ...string) [][]string {
	var lines = strings.Split(text, "\n")
	var i int
	var cols []span
	var found = -1
	for i = 0; i < len(lines); i++ {
		cols = parseRuler(lines[i])
		if cols == nil {
			continue
		}
		// The header is the line ABOVE the ruler. Some tables stack two header
		// lines; both are searched, which costs nothing and covers
		// "show spanning-tree", whose column names are split across two rows.
		var head = lines[i]
		if i > 0 {
			head = lines[i-1]
		}
		if i > 1 {
			head = lines[i-2] + "\n" + head
		}
		var w string
		var ok = true
		for _, w = range headerWords {
			if !strings.Contains(head, w) {
				ok = false
				break
			}
		}
		if ok {
			found = i
			break
		}
	}
	if found < 0 {
		return nil
	}

	var out [][]string
	for i = found + 1; i < len(lines); i++ {
		var row = lines[i]
		if strings.TrimSpace(row) == "" {
			break
		}
		var cells = make([]string, len(cols))
		var j int
		for j = range cols {
			cells[j] = cell(row, cols[j])
		}
		if cells[0] == "" {
			// No key in the first column: the table has ended and what follows
			// belongs to the next block.
			break
		}
		out = append(out, cells)
	}
	return out
}

// labelValues reads every "Label : value" out of a block, which is how this
// range prints every non-tabular figure.
//
// TWO PAIRS PER LINE IS THE NORM here, not an edge case, and reading only the
// first is how a value goes missing without anything looking wrong. CAPTURED:
//
//	Software revision  : R.11.121         Base MAC Addr      : 001ffe-007980
//	MST Configuration Revision : 0        Switch Priority : 32768
//
// The bridge priority — which the apply path needs — lives in the SECOND pair
// of its line. So a line is cut on runs of two-or-more spaces (the alignment
// padding this CLI uses, which never appears inside a value) and each piece is
// examined. A colon with nothing before it continues the previous piece: the
// padding sometimes falls between the label and its own colon.
func labelValues(text string) map[string]string {
	var out = map[string]string{}
	var line string
	for _, line = range strings.Split(text, "\n") {
		var chunks = splitWide(strings.TrimSpace(line))
		var i int
		for i = 0; i < len(chunks); i++ {
			var colon = strings.Index(chunks[i], ":")
			if colon < 0 {
				continue
			}
			var label = normalizeLabel(chunks[i][:colon])
			var value = strings.TrimSpace(chunks[i][colon+1:])
			if label == "" && i > 0 {
				label = strings.TrimSpace(chunks[i-1])
			}
			if value == "" && i+1 < len(chunks) {
				i++
				value = strings.TrimSpace(chunks[i])
			}
			if label != "" {
				var _, seen = out[label]
				if !seen {
					out[label] = value
				}
			}
		}
	}
	return out
}

// normalizeLabel drops the DEFAULT this CLI prints inside the label itself.
//
// CAPTURED in "show spanning-tree config":
//
//	STP Enabled [No] : Yes
//	Force Version [MSTP-operation] : RSTP-operation
//
// The bracket is the factory value, shown so an operator can see what has been
// changed. It is part of the label as printed and not part of its name, and
// leaving it in would make every lookup depend on a default that a firmware
// revision is free to change.
func normalizeLabel(s string) string {
	var open = strings.Index(s, "[")
	if open >= 0 && strings.Contains(s[open:], "]") {
		s = s[:open]
	}
	return strings.TrimSpace(s)
}

// splitWide cuts on runs of two or more spaces.
func splitWide(s string) []string {
	var out []string
	var start int
	var i int
	for i = 0; i+1 < len(s); i++ {
		if s[i] != ' ' || s[i+1] != ' ' {
			continue
		}
		if i > start {
			out = append(out, s[start:i])
		}
		for i < len(s) && s[i] == ' ' {
			i++
		}
		start = i
		i--
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

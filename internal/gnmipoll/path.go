package gnmipoll

import (
	"sort"
	"strings"
)

// gNMI paths: parse, format, join.
//
// PURE, no I/O, and the only file in this package a mistake in would be invisible
// on screen. A path that parses one element short does not fail — it produces a
// sample attributed to the wrong port, or to no port, and the graph that results
// looks exactly like a real one.
//
// # Why a parser at all, rather than string prefixes
//
// Because of the keys. `/interfaces/interface[name=Ethernet49/1]/state/counters/
// in-octets` contains a SLASH INSIDE A KEY VALUE — Arista names its breakout
// ports that way, and so does every platform that splits a 40G cage into four
// — so splitting the string on "/" attributes Ethernet49/1's counters to a port
// called "Ethernet49" that may also exist. That single case is the reason this
// is a scanner and not a strings.Split.

// PathElem is one element of a gNMI path, with its keys.
type PathElem struct {
	Name string
	// Keys: the list keys, e.g. {"name": "Ethernet49"}. Nil on an ordinary
	// container element.
	Keys map[string]string
}

// Path is a gNMI path: an ordered list of elements.
//
// A gNMI path has no leading slash of its own and no notion of absolute versus
// relative — a notification's prefix and its updates are both just element
// lists, and it is [Join] that gives them their relationship. The leading slash
// in the string form is a convention of the textual spelling only.
type Path []PathElem

// ParsePath reads the textual spelling used by gnmic and every gNMI example.
//
// An empty string is a valid EMPTY path (a notification may carry no prefix),
// not an error.
//
// A malformed path is an ERROR, never a partial path. That choice is the whole
// point of this function: a half-parsed path silently drops the element carrying
// the port key, and the sample it produces is attributed to whatever remains.
// Refusing is the only outcome a caller can notice.
func ParsePath(s string) (Path, error) {
	var out = Path{}
	var i int
	var n = len(s)
	// A leading slash is the convention, not part of the first element.
	if n > 0 && s[0] == '/' {
		i = 1
	}
	var elem strings.Builder
	var keys map[string]string
	var inKey bool
	// afterKey: the previous character closed a bracket. Only another '[', a '/'
	// or the end of the string may follow.
	//
	// THE ONE HOLE THIS PARSER HAD. Without it, `/interfaces/interface[name=Et1]
	// junk/state` parsed to an element called "interfacejunk": the key was read
	// correctly, the trailing text was appended to the element NAME, and no error
	// was raised. Every other malformation here is refused, and this one was not —
	// which made it the only way to get a path that looks structurally fine and
	// names something that does not exist. It surfaces far away and much later, as
	// a leaf that matches nothing in any table, i.e. as a series that is silently
	// never published.
	var afterKey bool
	var keyBuf strings.Builder
	var flush = func() error {
		if elem.Len() == 0 {
			// An empty element means a doubled or trailing slash. It is not
			// harmless: `/interfaces//interface` and `/interfaces/interface`
			// would otherwise parse the same, and one of them came from a bug.
			return errBadPath
		}
		out = append(out, PathElem{Name: elem.String(), Keys: keys})
		elem.Reset()
		keys = nil
		return nil
	}
	for ; i < n; i++ {
		var c = s[i]
		if inKey {
			if c == ']' {
				var k, v, found = strings.Cut(keyBuf.String(), "=")
				if !found || k == "" {
					// "[name]" with no value, or "[=x]". Both are the kind of
					// typo that would otherwise produce a keyless element and a
					// sample with no port.
					return nil, errBadPath
				}
				if keys == nil {
					keys = map[string]string{}
				}
				keys[k] = v
				keyBuf.Reset()
				inKey = false
				afterKey = true
				continue
			}
			// EVERYTHING inside the brackets is literal, slashes included: this
			// is where "Ethernet49/1" survives.
			keyBuf.WriteByte(c)
			continue
		}
		switch c {
		case '[':
			if elem.Len() == 0 {
				// A key on no element at all.
				return nil, errBadPath
			}
			inKey = true
			afterKey = false
		case '/':
			var err = flush()
			if err != nil {
				return nil, err
			}
			afterKey = false
		default:
			if afterKey {
				// Text immediately after "]". See the declaration of afterKey:
				// this is the malformation that used to be folded into the element
				// name instead of refused.
				return nil, errBadPath
			}
			elem.WriteByte(c)
		}
	}
	if inKey {
		// Unclosed bracket. The remainder was being read as a key value, so
		// whatever it named is lost — refuse rather than return the prefix.
		return nil, errBadPath
	}
	if n == 0 {
		// The empty path, which is legitimate: a notification may carry no
		// prefix at all, and every update is then absolute on its own.
		return out, nil
	}
	// The LAST element is flushed unconditionally, which is what makes a trailing
	// slash an error rather than a silently shorter path — flush refuses an empty
	// element. "/interfaces/" and "/interfaces" must not parse the same: one
	// of the two came from a string built by concatenation, and that is exactly
	// the bug worth catching here rather than three layers up.
	var err = flush()
	if err != nil {
		return nil, err
	}
	return out, nil
}

const errBadPath = gnmiError("malformed gNMI path")

// String renders the textual spelling. Keys are emitted in SORTED order.
//
// Sorted because Go map iteration is randomized, and a path used as a map key or
// compared in a test would otherwise be equal to itself only sometimes. It costs
// one sort on a map that has one entry in practice.
func (p Path) String() string {
	var b strings.Builder
	var e PathElem
	for _, e = range p {
		b.WriteByte('/')
		b.WriteString(e.Name)
		if len(e.Keys) == 0 {
			continue
		}
		var names = make([]string, 0, len(e.Keys))
		var k string
		for k = range e.Keys {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k = range names {
			b.WriteByte('[')
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(e.Keys[k])
			b.WriteByte(']')
		}
	}
	return b.String()
}

// Join concatenates a notification prefix and an update's relative path.
//
// The one function in this file with a wrong answer that is plausible: a
// notification carries its port key in the PREFIX and its leaf in the update, so
// a collector that read the update alone would find no port, and one that read
// the prefix alone would find no metric. Neither produces an error; both produce
// a graph.
//
// It COPIES rather than appending onto the prefix slice. Appending would let two
// updates of the same notification share a backing array, and the second one
// would overwrite the first's elements — a bug that shows up only on
// notifications carrying more than one update, i.e. on real devices and not in
// the first test anybody writes.
func Join(prefix, rel Path) Path {
	var out = make(Path, 0, len(prefix)+len(rel))
	out = append(out, prefix...)
	out = append(out, rel...)
	return out
}

// Key returns the value of a key carried by the LAST element that has it.
//
// Last rather than first, because a path may legitimately carry several keyed
// elements (`/interfaces/interface[name=X]/subinterfaces/subinterface[index=0]`)
// and the innermost is the one describing the thing being reported.
func (p Path) Key(elem, key string) string {
	var out string
	var e PathElem
	for _, e = range p {
		if e.Name != elem {
			continue
		}
		var v, ok = e.Keys[key]
		if ok {
			out = v
		}
	}
	return out
}

// Names returns the element names, keys dropped.
//
// What the leaf tables are indexed by: the METRIC a path names is decided by its
// element names alone (`state/counters/in-octets`), while the keys decide which
// port it belongs to. Keeping the two questions apart is what stops a table from
// having to carry a port name in it.
func (p Path) Names() []string {
	var out = make([]string, 0, len(p))
	var e PathElem
	for _, e = range p {
		out = append(out, e.Name)
	}
	return out
}

package driver

import (
	"context"
	"net/url"
	"regexp"
	"strings"
)

// HTML forms: what every connector for a web-only device needs.
//
// These switches have no API: the only way to configure them is to replay what a
// browser does. Replaying a form is not posting two guessed fields — it's sending
// back ALL of its hidden fields, including the session tokens and sequence
// counters the UI slips into it. A POST missing one of them is rejected, often
// without any message.
//
// Hence this parser: it reads the form exactly as served, and the connector only
// overrides the fields it wants to change.

var (
	reForm      = regexp.MustCompile(`(?is)<form\b([^>]*)>(.*?)</form>`)
	reInput     = regexp.MustCompile(`(?is)<input\b([^>]*)>`)
	reSelect    = regexp.MustCompile(`(?is)<select\b([^>]*)>(.*?)</select>`)
	reOptionSel = regexp.MustCompile(`(?is)<option\b([^>]*\bselected\b[^>]*)>`)
	reAttr      = regexp.MustCompile(`(?is)([a-zA-Z0-9_:-]+)\s*=\s*"([^"]*)"|([a-zA-Z0-9_:-]+)\s*=\s*'([^']*)'`)
)

// Form is a form exactly as the device served it.
type Form struct {
	Action string
	Method string
	Name   string
	ID     string
	// Fields carries all the fields in their order of appearance, including
	// their original values.
	Fields url.Values
	// Order keeps the order: some device UIs are sensitive to parameter order,
	// and respecting it costs nothing.
	Order []string
	// Password lists the password-type fields: this is what lets a login form
	// be recognized without guessing its name.
	Password []string
}

func attrs(s string) map[string]string {
	var out = map[string]string{}
	var m []string
	for _, m = range reAttr.FindAllStringSubmatch(s, -1) {
		if m[1] != "" {
			out[strings.ToLower(m[1])] = m[2]
			continue
		}
		out[strings.ToLower(m[3])] = m[4]
	}
	return out
}

// ParseForms reads all the forms on a page.
func ParseForms(body string) []Form {
	var out []Form
	var m []string
	for _, m = range reForm.FindAllStringSubmatch(body, -1) {
		var a = attrs(m[1])
		var f = Form{
			Action: a["action"],
			Method: strings.ToUpper(a["method"]),
			Name:   a["name"],
			ID:     a["id"],
			Fields: url.Values{},
		}
		if f.Method == "" {
			f.Method = "GET"
		}
		var im []string
		for _, im = range reInput.FindAllStringSubmatch(m[2], -1) {
			var ia = attrs(im[1])
			var name = ia["name"]
			if name == "" {
				continue
			}
			var typ = strings.ToLower(ia["type"])
			// A button that wasn't clicked isn't sent by a browser; including it
			// would trigger an action nobody asked for.
			if typ == "submit" || typ == "button" || typ == "reset" || typ == "image" {
				continue
			}
			// Unchecked checkboxes and radios: absent from the POST, just like in
			// a browser.
			if (typ == "checkbox" || typ == "radio") && !strings.Contains(strings.ToLower(im[1]), "checked") {
				continue
			}
			var _, seen = f.Fields[name]
			if !seen {
				f.Order = append(f.Order, name)
			}
			f.Fields.Set(name, ia["value"])
			if typ == "password" {
				f.Password = append(f.Password, name)
			}
		}
		var sm []string
		for _, sm = range reSelect.FindAllStringSubmatch(m[2], -1) {
			var sa = attrs(sm[1])
			var name = sa["name"]
			if name == "" {
				continue
			}
			var value string
			var om = reOptionSel.FindStringSubmatch(sm[2])
			if om != nil {
				value = attrs(om[1])["value"]
			}
			var _, seen = f.Fields[name]
			if !seen {
				f.Order = append(f.Order, name)
			}
			f.Fields.Set(name, value)
		}
		out = append(out, f)
	}
	return out
}

// LoginForm picks the login form: the one carrying a password-type field. If
// there are several, the first one — a login page never serves two.
func LoginForm(body string) (Form, bool) {
	var f Form
	for _, f = range ParseForms(body) {
		if len(f.Password) > 0 {
			return f, true
		}
	}
	return Form{}, false
}

// Describe summarizes the forms on a page for a session transcript. When a
// connector doesn't recognize a page, it must say what it SAW: "unexpected form"
// without describing it forces someone to bring up a browser to find out.
func Describe(body string) []string {
	var out []string
	var f Form
	for _, f = range ParseForms(body) {
		var label = f.Method + " " + f.Action
		if f.Name != "" {
			label = f.Name + " → " + label
		}
		out = append(out, label+" ["+strings.Join(f.Order, ", ")+"]")
	}
	return out
}

// PostForm returns the form with its fields overridden, ready to be posted.
// Fields not mentioned keep the value served by the device.
func (f Form) PostForm(override map[string]string) url.Values {
	var v = url.Values{}
	var k string
	var vals []string
	for k, vals = range f.Fields {
		v[k] = append([]string{}, vals...)
	}
	for k = range override {
		v.Set(k, override[k])
	}
	return v
}

// ResolveAction resolves the form's action relative to the page's URL. These UIs
// use relative paths under a session prefix (`/csNNNNNNNN/…`): resolving it by
// hand is the number one source of mistakes.
func (f Form) ResolveAction(pageURL string) string {
	var base, err = url.Parse(pageURL)
	if err != nil {
		return f.Action
	}
	if f.Action == "" {
		return base.Path
	}
	var act *url.URL
	act, err = url.Parse(f.Action)
	if err != nil {
		return f.Action
	}
	var res = base.ResolveReference(act)
	if res.RawQuery != "" {
		return res.Path + "?" + res.RawQuery
	}
	return res.Path
}

var _ = context.Background

package driver

import (
	"context"
	"net/url"
	"regexp"
	"strings"
)

// Formulaires HTML : ce dont a besoin tout connecteur d'un device qui
// n'expose que son interface web.
//
// Ces switchs n'ont pas d'API : la seule way de les configurer est de rejouer
// ce que fait un navigateur. Rejouer un formulaire, ce n'est pas poster deux
// champs devines — c'est renvoyer TOUS ses champs caches, y compris les jetons
// de session et les compteurs de sequence que l'UI y glisse. Un POST truncated de
// l'un d'eux est rejete, souvent sans message.
//
// Hence ce parseur : il lit le formulaire tel qu'il est servi, et le connecteur
// ne surcharge que les champs qu'il veut changer.

var (
	reForm      = regexp.MustCompile(`(?is)<form\b([^>]*)>(.*?)</form>`)
	reInput     = regexp.MustCompile(`(?is)<input\b([^>]*)>`)
	reSelect    = regexp.MustCompile(`(?is)<select\b([^>]*)>(.*?)</select>`)
	reOptionSel = regexp.MustCompile(`(?is)<option\b([^>]*\bselected\b[^>]*)>`)
	reAttr      = regexp.MustCompile(`(?is)([a-zA-Z0-9_:-]+)\s*=\s*"([^"]*)"|([a-zA-Z0-9_:-]+)\s*=\s*'([^']*)'`)
)

// Form est un formulaire tel que the device l'a servi.
type Form struct {
	Action string
	Method string
	Name   string
	ID     string
	// Fields porte tous les champs dans leur ordre d'apparition, valeurs
	// d'origine comprises.
	Fields url.Values
	// Order garde l'ordre : certaines UI d'device sont sensibles a l'ordre
	// des parameters, et le respecter ne costs rien.
	Order []string
	// Password liste les champs de type password : c'est ce qui permet de
	// reconnaitre un formulaire de connexion sans deviner son nom.
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

// ParseForms lit tous les formulaires d'une page.
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
			// Un bouton non clique n'est pas envoye par un navigateur ; l'inclure
			// declencherait une action que personne n'a demandee.
			if typ == "submit" || typ == "button" || typ == "reset" || typ == "image" {
				continue
			}
			// Cases et radios non cochees : absentes du POST, comme dans un
			// navigateur.
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

// LoginForm choisit le formulaire de connexion : celui qui porte un champ de
// type password. S'il y en a plusieurs, le premier — une page de connexion n'en
// sert jamais deux.
func LoginForm(body string) (Form, bool) {
	var f Form
	for _, f = range ParseForms(body) {
		if len(f.Password) > 0 {
			return f, true
		}
	}
	return Form{}, false
}

// Describe resume les formulaires d'une page pour un transcript de session.
// Quand un connecteur ne reconnait pas une page, il doit dire ce qu'il A VU :
// « formulaire inattendu » sans le decrire oblige a ressortir un navigateur.
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

// PostForm renvoie le formulaire avec les champs surcharges, pret a be poste.
// Les champs non mentionnes gardent la valeur servie par the device.
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

// ResolveAction resout l'action du formulaire par rapport a l'URL de la page.
// Ces UI utilisent des chemins relatifs sous un prefix de session
// (`/csNNNNNNNN/…`) : le resoudre a la main est la first source d'erreur.
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

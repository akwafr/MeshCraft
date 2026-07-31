// Package debuglog garde en memory les dernieres lignes de journal de
// l'APPLICATION, et les diffuse en direct.
//
// A ne pas confondre avec le [[Journal d'evenements]], qui parle du PARC a
// l'exploitant. Ici on parle de meshcraft a qui le diagnostique : « connecteur
// instanton : HTTP 200 sans code state » n'matters pas l'administrateur d'un
// site, mais c'est la seule chose qui compte quand on cherche pourquoi une pose
// fails.
//
// Voir docs/01-Architecture/Monitoring unifie et pipelines §6.
//
// EN MEMORY, jamais en base. Un journal de debug qui written en base ajoute de la
// charge au moment precise where quelque chose va mal — i.e. au pire moment
// possible.
package debuglog

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Line est une ligne de journal telle qu'elle sera montree.
type Line struct {
	Seq   uint64            `json:"seq"`
	TS    time.Time         `json:"ts"`
	Level string            `json:"level"`
	Msg   string            `json:"msg"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// Ring garde les N dernieres lignes et reveille les abonnes.
//
// La borne est ce qui rend la chose sure : un composant qui part en boucle
// remplit l'anneau et ecrase les lignes anciennes, il ne remplit jamais la
// memory du controleur.
type Ring struct {
	mu      sync.RWMutex
	lines   []Line
	next    int
	seq     uint64
	subs    map[chan Line]struct{}
	maxSubs int
}

// NewRing construit l'anneau. `size` est le nombre de lignes conservees.
func NewRing(size int) *Ring {
	if size <= 0 {
		size = 2000
	}
	return &Ring{lines: make([]Line, size), subs: map[chan Line]struct{}{}, maxSubs: 16}
}

// sensitive dit si une key d'attribut porte probablement un secret.
//
// Le filtrage est fait sur le NOM, pas sur la valeur : chercher un secret dans
// une valeur suppose de savoir a quoi il ressemble, ce qui est faux par
// construction. Un mot de passe qui transite dans un journal de debug est un mot
// de passe fuite — et ce flux est consultable dans un navigateur.
func sensitive(key string) bool {
	var k = strings.ToLower(key)
	var needle string
	for _, needle = range []string{"password", "secret", "token", "cred", "passwd", "key"} {
		if strings.Contains(k, needle) {
			return true
		}
	}
	return false
}

// Add enregistre une ligne et la pousse aux abonnes.
func (r *Ring) Add(l Line) {
	r.mu.Lock()
	r.seq++
	l.Seq = r.seq
	r.lines[r.next] = l
	r.next = (r.next + 1) % len(r.lines)
	var subs = make([]chan Line, 0, len(r.subs))
	var c chan Line
	for c = range r.subs {
		subs = append(subs, c)
	}
	r.mu.Unlock()

	for _, c = range subs {
		// Envoi NON bloquant : un abonne lent — un navigateur sur un lien
		// mediocre — ne doit jamais ralentir le code qui journalise. Il perd des
		// lignes, ce qui est exactement le bon compromis pour du debug.
		select {
		case c <- l:
		default:
		}
	}
}

// Snapshot rend les lignes conservees, de la plus ancienne a la plus recente.
func (r *Ring) Snapshot(minLevel string) []Line {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out = make([]Line, 0, len(r.lines))
	var i int
	for i = 0; i < len(r.lines); i++ {
		var l = r.lines[(r.next+i)%len(r.lines)]
		if l.TS.IsZero() || !AtLeast(l.Level, minLevel) {
			continue
		}
		out = append(out, l)
	}
	return out
}

var rank = map[string]int{"DEBUG": 0, "INFO": 1, "WARN": 2, "ERROR": 3}

// AtLeast dit si un niveau atteint le seuil requested.
func AtLeast(level, min string) bool {
	if min == "" {
		return true
	}
	return rank[level] >= rank[min]
}

// Subscribe ouvre un flux. La fonction rendue doit be appelee pour liberer.
//
// Le nombre d'abonnes est BORNE : chaque flux tient un canal et une goroutine
// HTTP. Un onglet oublie ne doit pas couter indefiniment.
func (r *Ring) Subscribe() (<-chan Line, func(), bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.subs) >= r.maxSubs {
		return nil, func() {}, false
	}
	var c = make(chan Line, 256)
	r.subs[c] = struct{}{}
	return c, func() {
		r.mu.Lock()
		delete(r.subs, c)
		close(c)
		r.mu.Unlock()
	}, true
}

// Handler est un slog.Handler qui written dans l'anneau EN PLUS du handler
// habituel.
//
// « En plus » et non « a la place » : les logs doivent continuer d'aller sur la
// sortie standard, where les recupere l'exploitation du conteneur. Une zone de
// debug qui detournerait le flux ferait perdre les journaux au moment where on en
// a le plus besoin.
type Handler struct {
	next  slog.Handler
	ring  *Ring
	attrs []slog.Attr
	group string
}

// NewHandler emballe un handler existant.
func NewHandler(next slog.Handler, ring *Ring) *Handler {
	return &Handler{next: next, ring: ring}
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *Handler) Handle(ctx context.Context, rec slog.Record) error {
	var line = Line{
		TS: rec.Time, Level: rec.Level.String(), Msg: rec.Message,
		Attrs: map[string]string{},
	}
	var a slog.Attr
	for _, a = range h.attrs {
		putAttr(line.Attrs, h.group, a)
	}
	rec.Attrs(func(at slog.Attr) bool {
		putAttr(line.Attrs, h.group, at)
		return true
	})
	if len(line.Attrs) == 0 {
		line.Attrs = nil
	}
	h.ring.Add(line)
	return h.next.Handle(ctx, rec)
}

func putAttr(dst map[string]string, group string, a slog.Attr) {
	var key = a.Key
	if group != "" {
		key = group + "." + key
	}
	if sensitive(key) {
		dst[key] = "[masque]"
		return
	}
	dst[key] = a.Value.String()
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var out = *h
	out.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	out.next = h.next.WithAttrs(attrs)
	return &out
}

func (h *Handler) WithGroup(name string) slog.Handler {
	var out = *h
	out.group = name
	out.next = h.next.WithGroup(name)
	return &out
}

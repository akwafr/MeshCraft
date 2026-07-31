package store

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Reserved first URL segments — a site slug must not collide with SPA or API roots.
var reservedSiteSlugs = map[string]bool{
	"config": true, "login": true, "api": true, "assets": true, "static": true,
	"health": true, "metrics": true, "s": true, "admin": true, "auth": true,
	"well-known": true,
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)
var multiHyphen = regexp.MustCompile(`-+`)

// SlugifySite turns a display name into a candidate URL slug (lowercase, hyphens).
// Caller still must ensure uniqueness and reserved-name avoidance via AllocSiteSlug.
func slugifySite(name string) string {
	var b strings.Builder
	var r rune
	for _, r = range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	var s = multiHyphen.ReplaceAllString(nonSlug.ReplaceAllString(b.String(), "-"), "-")
	s = strings.Trim(s, "-")
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	return s
}

func validSiteSlug(slug string) bool {
	if len(slug) < 2 || len(slug) > 48 {
		return false
	}
	if reservedSiteSlugs[slug] {
		return false
	}
	var i int
	for i = 0; i < len(slug); i++ {
		var c = slug[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if c == '-' && i > 0 && i < len(slug)-1 {
			continue
		}
		return false
	}
	return slug[0] != '-' && slug[len(slug)-1] != '-'
}

// AllocSiteSlug picks a free slug from a preferred base (usually slugifySite(name)).
// excludeID is the site being renamed (empty on create) so it may keep its own slug.
func (s *Store) AllocSiteSlug(ctx context.Context, preferred, excludeID string) (string, error) {
	var base = preferred
	if !validSiteSlug(base) {
		base = slugifySite(preferred)
	}
	if !validSiteSlug(base) {
		base = "site"
	}
	if reservedSiteSlugs[base] {
		base = "site-" + base
	}
	if len(base) > 48 {
		base = strings.Trim(base[:48], "-")
	}

	var n int
	for n = 0; n < 1000; n++ {
		var cand = base
		if n > 0 {
			var suffix = "-" + strconv.Itoa(n+1)
			var room = 48 - len(suffix)
			if room < 2 {
				cand = "site" + suffix
			} else {
				cand = strings.Trim(base[:min(len(base), room)], "-") + suffix
			}
		}
		var taken bool
		var err = s.pool.QueryRow(ctx,
			`select exists(
			   select 1 from sites
			    where slug = $1
			      and ($2 = '' or id <> $2::uuid)
			 )`, cand, excludeID).Scan(&taken)
		if err != nil {
			return "", err
		}
		if !taken {
			return cand, nil
		}
	}
	return "", errors.New("could not allocate a free site slug")
}

-- URL slug for sites: readable first path segment (/{slug}/equipment).
-- Distinct from display name so renaming the label need not break bookmarks
-- unless the operator also changes the slug.

alter table sites add column slug text;

-- Backfill from name: lowercase, non-alnum → hyphen, collapse hyphens, trim.
update sites set slug = trim(both '-' from regexp_replace(
	regexp_replace(lower(name), '[^a-z0-9]+', '-', 'g'),
	'-+', '-', 'g'));

-- Empty or too-short after scrub → fall back to id prefix.
update sites set slug = 'site-' || substr(replace(id::text, '-', ''), 1, 8)
 where slug is null or length(slug) < 2;

-- Disambiguate collisions with -2, -3, … (stable order by created_at, id).
with ranked as (
	select id,
	       slug,
	       row_number() over (partition by slug order by created_at, id) as n
	  from sites
)
update sites s
   set slug = case when r.n = 1 then r.slug else r.slug || '-' || r.n::text end
  from ranked r
 where s.id = r.id
   and r.n > 1;

-- Reserved first segments must not be site slugs (SPA + API).
update sites set slug = 'site-' || slug
 where slug in (
	'config', 'login', 'api', 'assets', 'static', 'health', 'metrics',
	's', 'admin', 'auth', 'well-known'
 );

alter table sites alter column slug set not null;
alter table sites add constraint sites_slug_key unique (slug);
alter table sites add constraint sites_slug_format
	check (slug ~ '^[a-z0-9]([a-z0-9-]{0,46}[a-z0-9])?$' and length(slug) between 2 and 48);

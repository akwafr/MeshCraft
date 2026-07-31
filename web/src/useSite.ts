import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useParams } from "react-router-dom";

import { get, type Site } from "./api";
import { LAST_SITE_KEY } from "./paths";

export type ResolvedSite = {
  /** UUID for API calls. */
  id: string;
  slug: string;
  name: string;
};

/**
 * Resolve the URL `:siteSlug` (or legacy UUID) against the sites list.
 * Returns undefined while loading or when the slug is unknown.
 */
export function useSite(): {
  site: ResolvedSite | undefined;
  sites: Site[] | undefined;
  siteSlug: string | undefined;
  loading: boolean;
} {
  const { siteSlug } = useParams<{ siteSlug?: string }>();
  const { data: sites, isLoading } = useQuery({
    queryKey: ["sites"],
    queryFn: () => get<Site[]>("/api/sites"),
  });

  const site = useMemo(() => {
    if (!siteSlug || !sites) return undefined;
    const bySlug = sites.find((s) => s.slug === siteSlug);
    if (bySlug) {
      localStorage.setItem(LAST_SITE_KEY, bySlug.slug);
      return { id: bySlug.id, slug: bySlug.slug, name: bySlug.name };
    }
    // Legacy bookmarks still carry a UUID in the first segment.
    const byId = sites.find((s) => s.id === siteSlug);
    if (byId) {
      localStorage.setItem(LAST_SITE_KEY, byId.slug);
      return { id: byId.id, slug: byId.slug, name: byId.name };
    }
    return undefined;
  }, [siteSlug, sites]);

  return { site, sites, siteSlug, loading: isLoading };
}

/** Site UUID for API hooks — empty string when unresolved (queries should disable). */
export function useSiteId(): string {
  const { site } = useSite();
  return site?.id ?? "";
}

/**
 * A shared dashboard, seen by someone with no account.
 *
 * # What this page is not
 *
 * It is not the application with the chrome removed. It never renders Layout,
 * so there is no site selector, no navigation, no user menu — nothing that
 * could lead somewhere requiring a session. It calls exactly one endpoint,
 * `/api/public/dashboards/{token}`, which is the only route in the product that
 * answers without one, and it holds no credentials of any kind.
 *
 * The tiles are the same components the authenticated page uses, with
 * `publicView` set: links become plain text, the event log drops its author
 * column, and the alert tile says it is showing device and link alerts only —
 * because proxies, being an instance resource rather than a site one, are not
 * served here at all (see handlePublicDashboard, Go side, for the full list of
 * what this route can and cannot reach).
 *
 * # Why it is mounted before the login gate
 *
 * App.tsx checks this route BEFORE it looks at the session, otherwise a visitor
 * with no cookie would be shown the login form and the whole feature would be a
 * link that never works.
 */
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import type { Device, DeviceLink, Event, TrafficSeries, Vlan } from "../api";
import { fetchPublicDashboard } from "../dashboardApi";
import DashboardGrid from "../dashboardGrid";
import type { Tile } from "../dashboardLayout";
import type { TileData } from "../dashboardTiles";

/** How often a shared page re-reads. Half a minute: this is a wall display, and
 *  the site's own screens refresh on a change bus a public visitor has no
 *  access to (the stream route is behind the session, and stays there). */
const PUBLIC_REFRESH_MS = 30000;

/** The traffic window, read from the first traffic tile. One window for the
 *  whole page: the public route serves one series per request, and pretending
 *  otherwise would show two charts labelled differently over the same data. */
function windowOf(tiles: Tile[]): number {
  const tile = tiles.find((t) => t.type === "chart.traffic");
  const h = tile?.options?.hours;
  return typeof h === "number" ? h : 24;
}

/**
 * `token` is a PROP and not a route param: this page is rendered above the
 * router's `<Routes>` tree (App.tsx answers the share path before it consults
 * the session), so `useParams` would see nothing here.
 */
export default function DashboardPublic({ token }: { token: string }) {
  const { t } = useTranslation();
  // Local tile state, so the window chips and the log filters work on a page
  // nobody can save. Nothing here is ever written back — there is no route to
  // write to.
  const [local, setLocal] = useState<Tile[] | null>(null);
  const hours = windowOf(local ?? []);

  const { data, error, isLoading } = useQuery({
    queryKey: ["public-dashboard", token, hours],
    queryFn: () => fetchPublicDashboard(token, hours),
    refetchInterval: PUBLIC_REFRESH_MS,
    // One shot. A revoked link is a 404 that will not become a 200 by trying
    // again, and retrying would turn a dead link into a burst of requests.
    retry: false,
  });

  const tiles = local ?? data?.tiles ?? [];

  const tileData = useMemo<TileData>(() => {
    // The projected records are deliberately SMALLER than the authenticated
    // types (no management address, no site id, no event author): the tiles
    // read only the fields the projection carries, and the ones they fall back
    // on — `mgmt_ip` in the fleet table — degrade to the model name when
    // absent. The cast says that out loud rather than widening the shared types
    // to make every field optional for everybody.
    return {
      devices: (data?.devices ?? []) as unknown as Device[],
      vlans: (data?.vlans ?? []) as unknown as Vlan[],
      links: (data?.links ?? []) as unknown as DeviceLink[],
      events: (data?.events ?? []) as unknown as Event[],
      // proxies deliberately absent — see the file header.
      trafficFor: () => data?.traffic as TrafficSeries | undefined,
      optics: data?.optics as TileData["optics"],
    };
  }, [data]);

  if (isLoading) return <div className="empty">{t("common.loading")}</div>;

  if (error || !data) {
    // One message for every reason a token fails to resolve — unknown, revoked,
    // rotated, mistyped. The server already refuses to distinguish them; saying
    // more here would put the distinction back.
    return (
      <div className="app" style={{ padding: 24 }}>
        <h1>{t("dashboards.publicGoneTitle")}</h1>
        <p className="note">{t("dashboards.publicGone")}</p>
      </div>
    );
  }

  return (
    <div style={{ padding: 24, maxWidth: 1800, margin: "0 auto" }}>
      <div className="toolbar">
        <h1>
          {data.name}
          <span className="sub">{data.site_name}</span>
        </h1>
        <span className="badge">{t("dashboards.publicBadge")}</span>
      </div>
      <p className="note" style={{ marginTop: 0 }}>
        {t("dashboards.publicNotice")}
      </p>
      <DashboardGrid
        tiles={tiles}
        data={tileData}
        siteSlug=""
        publicView
        editing={false}
        // Not an editing hook: the grid routes tile OPTIONS through onChange,
        // which is how the window chips and the log filters work here. The
        // result lives in this component's state and goes nowhere else.
        onChange={setLocal}
      />
    </div>
  );
}

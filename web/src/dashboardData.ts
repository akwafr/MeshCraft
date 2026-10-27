/**
 * What an AUTHENTICATED dashboard fetches, and nothing more.
 *
 * The tiles on the page decide the queries: a dashboard of six stat tiles asks
 * for devices and stops there, and one with no traffic chart never touches the
 * series route. That is the same rule the public route enforces server-side
 * (store.DatasetsFor) — here it is a performance decision rather than a
 * boundary, since the caller already has a session and could ask for any of it
 * directly.
 *
 * Every query is DECLARED unconditionally and switched with `enabled`, because
 * hooks cannot be called conditionally. The cost of a disabled query is one
 * object; the cost of getting this wrong the other way — hooks in an `if` — is
 * a page that crashes when a tile is removed.
 */
import { useQueries, useQuery } from "@tanstack/react-query";

import {
  get,
  useSiteLive,
  type Device,
  type DeviceLink,
  type Event,
  type OpticsAlerts,
  type ProxyInfo,
  type TrafficSeries,
  type Vlan,
} from "./api";
import type { Tile } from "./dashboardLayout";
import { datasetsFor, type TileData } from "./dashboardTiles";

/**
 * How many log lines a dashboard holds.
 *
 * One query serves every tile that shows events — the log table, the "recent
 * events" list, and the three counters — and each filters this window itself.
 * 300 is what the old Monitoring page asked for, and it is the same number the
 * public route serves, so the two pages count the same things.
 */
const EVENT_WINDOW = 300;

/** The traffic windows a tile may hold, mirroring what the server accepts. */
const ALLOWED_HOURS = [24, 72, 168, 720];

/**
 * Distinct traffic windows this dashboard displays.
 *
 * Two traffic tiles set to 24 h share one query; one set to 24 h and one to 7 d
 * make two. Sorted so the array identity is stable across renders — an
 * unsorted list would reorder itself with the tiles and restart the queries.
 */
function trafficWindows(tiles: Tile[]): number[] {
  const out = new Set<number>();
  tiles.forEach((t) => {
    if (t.type !== "chart.traffic") return;
    const h = t.options?.hours;
    out.add(typeof h === "number" && ALLOWED_HOURS.includes(h) ? h : 24);
  });
  return Array.from(out).sort((a, b) => a - b);
}

export function useDashboardData(siteId: string, tiles: Tile[]): TileData {
  const needs = datasetsFor(tiles.map((t) => t.type));
  // Real-time: a dashboard is a screen kept open, often on a wall. It follows
  // the site without being asked (cf. useSiteLive).
  const refetchInterval = useSiteLive(siteId);
  const on = (want: boolean) => !!siteId && want;

  const { data: devices } = useQuery({
    queryKey: ["devices", siteId],
    queryFn: () => get<Device[]>(`/api/sites/${siteId}/devices`),
    enabled: on(needs.has("devices") || needs.has("traffic")),
    refetchInterval,
  });
  const { data: vlans } = useQuery({
    queryKey: ["vlans", siteId],
    queryFn: () => get<Vlan[]>(`/api/sites/${siteId}/vlans`),
    enabled: on(needs.has("vlans")),
  });
  const { data: links } = useQuery({
    queryKey: ["links", siteId],
    queryFn: () => get<DeviceLink[]>(`/api/sites/${siteId}/links`),
    enabled: on(needs.has("links")),
    refetchInterval,
  });
  const { data: events } = useQuery({
    queryKey: ["events", siteId, "dashboard", EVENT_WINDOW],
    queryFn: () => get<Event[]>(`/api/sites/${siteId}/events?limit=${EVENT_WINDOW}`),
    enabled: on(needs.has("events")),
    refetchInterval,
  });
  // Proxies are an INSTANCE resource: no site filter, and no site id in the key.
  // Only the alert tiles read them, and only they pay for the call.
  const wantsAlerts = tiles.some(
    (t) => t.type === "list.alerts" || t.type === "stat.alerts",
  );
  const { data: proxies } = useQuery({
    queryKey: ["proxies"],
    queryFn: () => get<ProxyInfo[]>("/api/proxies"),
    enabled: wantsAlerts,
  });

  const { data: optics } = useQuery({
    queryKey: ["optics-alerts", siteId],
    queryFn: () => get<OpticsAlerts>(`/api/sites/${siteId}/optics/rx-alerts`),
    enabled: on(needs.has("optics")),
    // A minute, like traffic: the underlying levels are sampled every 30-60 s,
    // and the floor moves on the scale of days.
    refetchInterval: 60000,
  });

  const windows = trafficWindows(tiles);
  const traffic = useQueries({
    queries: windows.map((hours) => ({
      queryKey: ["traffic", siteId, hours],
      queryFn: () => get<TrafficSeries>(`/api/sites/${siteId}/traffic?hours=${hours}`),
      enabled: !!siteId,
      // A minute, like the old page: the series is built from counters sampled
      // at best every minute, so asking faster returns the same points.
      refetchInterval: 60000,
    })),
  });

  return {
    devices: devices ?? [],
    vlans: vlans ?? [],
    links: links ?? [],
    events: events ?? [],
    proxies: proxies ?? [],
    optics,
    trafficFor: (hours: number) => {
      const at = windows.indexOf(hours);
      return at < 0 ? undefined : (traffic[at]?.data as TrafficSeries | undefined);
    },
  };
}

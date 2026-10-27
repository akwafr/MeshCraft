/**
 * The dashboard API, typed.
 *
 * Kept out of api.ts on purpose: that file is the shared client every screen
 * imports, and this feature owns its own routes. It imports the transport
 * helpers from there and adds nothing to them.
 */
import { del, get, patch, post, put } from "./api";
import type { Tile } from "./dashboardLayout";

/** A tile as the server stores it: a layout tile plus the row's identity. */
export interface StoredTile extends Tile {
  id: string;
}

export interface Dashboard {
  id: string;
  site_id: string;
  name: string;
  position: number;
  /**
   * A public link EXISTS. The token itself is never returned by any read — it
   * is shown once, by the call that mints it, and the server keeps only its
   * hash (migration 0041). A dashboard whose token was lost is rotated, not
   * recovered.
   */
  shared: boolean;
  share_created_at?: string;
  /** Last time someone opened the public link. Absent = never since it was minted. */
  share_last_used_at?: string;
  tiles: StoredTile[];
}

export const listDashboards = (siteId: string) =>
  get<Dashboard[]>(`/api/sites/${siteId}/dashboards`);

export const createDashboard = (siteId: string, name: string, tiles: Tile[]) =>
  post<Dashboard>(`/api/sites/${siteId}/dashboards`, { name, tiles });

export const renameDashboard = (id: string, name: string) =>
  patch<Dashboard>(`/api/dashboards/${id}`, { name });

export const moveDashboard = (id: string, position: number) =>
  patch<Dashboard>(`/api/dashboards/${id}`, { position });

/**
 * Replace a dashboard's whole arrangement. PUT, and the whole list every time:
 * a layout is one value (see store.SetDashboardTiles for why this is not a
 * diff).
 */
export const saveDashboardTiles = (id: string, tiles: Tile[]) =>
  put<Dashboard>(`/api/dashboards/${id}/tiles`, { tiles });

export const deleteDashboard = (id: string) => del<void>(`/api/dashboards/${id}`);

/**
 * Mint or ROTATE the public link. `site_admin` only, server-side.
 *
 * The response carries the clear token exactly once. Rotating kills the
 * previous link at the same instant — there is no window where both work —
 * so the UI must present this as what it is: the old URL stops working now.
 */
export const shareDashboard = (id: string) =>
  post<{ token: string; path: string }>(`/api/dashboards/${id}/share`);

export const unshareDashboard = (id: string) => del<void>(`/api/dashboards/${id}/share`);

// --- The public side ---

/**
 * What the unauthenticated route serves. A deliberately SMALLER shape than the
 * authenticated types: the server projects every record field by field (see
 * handlePublicDashboard), and this interface is the honest mirror of that — no
 * management addresses, no site id, no event authors, no proxies.
 */
export interface PublicDashboard {
  name: string;
  site_name: string;
  tiles: Tile[];
  /**
   * Which datasets the server actually computed, from the tiles this dashboard
   * carries. Lets the page tell "this site has no device" from "no tile here
   * reads devices" — the difference between an empty state and a wrong one.
   */
  datasets: string[];
  devices?: unknown[];
  vlans?: unknown[];
  links?: unknown[];
  events?: unknown[];
  traffic?: unknown;
  optics?: unknown;
}

/**
 * Fetch a shared dashboard by token. No credentials are sent and none are
 * expected: this is the one route in the product that answers without a
 * session.
 *
 * `hours` is the traffic window, and the server accepts only the four values
 * the chart offers — anything else falls back to 24 h rather than becoming a
 * new query shape against the series store.
 */
export const fetchPublicDashboard = (token: string, hours: number) =>
  get<PublicDashboard>(
    `/api/public/dashboards/${encodeURIComponent(token)}?hours=${hours}`,
  );

/**
 * The SPA path a share token opens. Mirrors publicDashboardPath in
 * internal/api/dashboard_handlers.go — the route, the copied link and the
 * server's answer must be the same string.
 */
export const publicDashboardPath = (token: string) => `/share/dashboard/${token}`;

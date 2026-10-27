import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useBreakpoint } from "../ui/breakpoint";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { deviceDetailPath, sitePath } from "../paths";
import { useSite, useSiteId } from "../useSite";
import {
  useSiteLive,
  del,
  get,
  patch,
  post,
  put,
  type Device,
  type DeviceLink,
  type LldpLink,
  type SiteLldp,
  type SitePlan,
  type Vlan,
} from "../api";
import { useSiteRole } from "../auth";
import {
  ActionButton,
  DeleteButton,
  ErrorMsg,
  Field,
  HelpTip,
  Modal,
  QuickTip,
  Switch,
} from "../components";
import {
  blindNow,
  expandPorts,
  fmtMbps,
  isSFPPort,
  isSFPRef,
  neverObserved,
  trafficColor,
  vlanMap,
  type PortRef,
} from "../network";
import { modelOf, modelShort } from "../modelLabel";
import {
  lldpIsolationBadge,
  lldpLinkKind,
  lldpLinkTooltip,
  lldpSilencePhrase,
} from "../lldp";
import { PortSquare, StatusDot } from "../netui";
import {
  ghostPeerLinkStroke,
  linkCapacityMbps,
  linkDiagColor,
  linkHalfShape,
  linkSpeedLabel,
  topologyLinkPaint,
  type TopologyLinkPaint,
} from "../topologyLinks";
import { TopologyLinkLabels } from "../TopologyLinkLabels";
import { LinkTrafficTip, type LinkTrafficHover } from "../LinkTrafficTip";
import { claimHoverTip, registerHoverTip, releaseHoverTip } from "../hoverExclusive";

type PlanBox = { x: number; y: number; w: number; h: number };

/**
 * THE BODY OF A LINK: two halves meeting tip to tip at its midpoint (ADR-0026).
 *
 * Each half is a shaft plus a triangle, and NOT one filled polygon, for a
 * reason that is not style: a polygon cannot be dashed, and the dash still
 * carries two meanings on this stroke (a link that is down, and an LLDP
 * neighbour seen from one side only). Stroking the shaft keeps the dash,
 * filling the point keeps the apex exactly on the midpoint.
 *
 * Both call sites below — the LLDP overlay and the declared links — draw their
 * body through this, so the two can never drift into two renderings of the
 * same cable.
 */
function LinkBody({
  x1,
  y1,
  x2,
  y2,
  paint,
}: {
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  paint: TopologyLinkPaint;
}) {
  const mx = (x1 + x2) / 2;
  const my = (y1 + y2) / 2;
  const halves = [
    { key: "a", ink: paint.inkA, s: linkHalfShape(x1, y1, mx, my, paint.strokeWidth, paint.heads) },
    { key: "b", ink: paint.inkB, s: linkHalfShape(x2, y2, mx, my, paint.strokeWidth, paint.heads) },
  ];
  return (
    <>
      {halves.map((h) => (
        <g key={h.key}>
          <line
            x1={h.s.x1}
            y1={h.s.y1}
            x2={h.s.x2}
            y2={h.s.y2}
            stroke={h.ink}
            strokeWidth={paint.strokeWidth}
            strokeDasharray={paint.strokeDasharray}
            opacity={paint.opacity}
            filter={paint.filter}
          />
          {h.s.head && (
            <polygon points={h.s.head} fill={h.ink} opacity={paint.opacity} />
          )}
        </g>
      ))}
    </>
  );
}

/** Point where the segment from the box center toward (tx,ty) meets the border. */
function boxEdgeToward(box: PlanBox, tx: number, ty: number): { x: number; y: number } {
  const cx = box.x + box.w / 2;
  const cy = box.y + box.h / 2;
  const dx = tx - cx;
  const dy = ty - cy;
  if (dx === 0 && dy === 0) return { x: cx, y: cy };
  const hw = box.w / 2;
  const hh = box.h / 2;
  const sx = dx === 0 ? Infinity : hw / Math.abs(dx);
  const sy = dy === 0 ? Infinity : hh / Math.abs(dy);
  const t = Math.min(sx, sy);
  return { x: cx + dx * t, y: cy + dy * t };
}

/** Link segment between two equipment boxes (endpoints on the borders). */
function linkBetweenBoxes(a: PlanBox, b: PlanBox): {
  x1: number; y1: number; x2: number; y2: number;
} {
  const acx = a.x + a.w / 2;
  const acy = a.y + a.h / 2;
  const bcx = b.x + b.w / 2;
  const bcy = b.y + b.h / 2;
  const p1 = boxEdgeToward(a, bcx, bcy);
  const p2 = boxEdgeToward(b, acx, acy);
  return { x1: p1.x, y1: p1.y, x2: p2.x, y2: p2.y };
}

const NODE_W = 176;
const NODE_CX = 88;
const NODE_CY = 38;
// DEFAULT height of a node, until it has been rendered and measured. A
// switch loaded with ports comfortably exceeds this value: settling for it
// would make the device spill out of its zone frame. The actual height is
// measured afterwards (nodeH).
const NODE_H = 104;
const ZONE_PAD = 16;
const ZONE_PAD_TOP = 26;

// LLDP_OFFSET: by how much an OBSERVED link shifts from the entered link it duplicates.
//
// Both join the same box edges: without an offset, an observation that confirms
// a declared link would be superimposed pixel-for-pixel, and one of the two
// would disappear. Five pixels are enough to tell them apart without the line
// looking like it connects to different equipment.
const LLDP_OFFSET = 5;

// Zoom: essential when a site has a lot of equipment.
const ZOOM_KEY = "meshcraft.planZoom";
const MIN_ZOOM = 0.3;
const MAX_ZOOM = 2;
const ZOOM_STEP = 1.2;
// Below this factor, nodes switch to compact view: port boxes are no longer
// readable, so we might as well not draw them (and skip paying the
// rendering cost for several thousand of them).
const LOD_ZOOM = 0.6;
// Sliding margin still available beyond the rightmost or lowest switch: the
// plan therefore never has a hard edge.
const WORLD_SLACK = 280;
const WORLD_MAX = 12000;

const LLDP_SHOW_KEY = "meshcraft.planLldpShow";
/** Same footprint as managed nodes so unknown peers read as equipment. */
const GHOST_W = NODE_W;
const GHOST_H = 72;
/** Clearance between an adjacent switch box and a default LLDP peer box. */
const GHOST_GAP = 32;

/**
 * Default position for an unknown LLDP peer: center sits on a rect expanded
 * around the known switch by half the peer box + gap, so the peer never
 * overlaps the adjacent equipment. Fans open to the east.
 */
function defaultLldpPeerPos(
  known: PlanBox,
  index: number,
  count: number,
): { x: number; y: number } {
  const expand: PlanBox = {
    x: known.x - (GHOST_W / 2 + GHOST_GAP),
    y: known.y - (GHOST_H / 2 + GHOST_GAP),
    w: known.w + GHOST_W + 2 * GHOST_GAP,
    h: known.h + GHOST_H + 2 * GHOST_GAP,
  };
  const kcx = known.x + known.w / 2;
  const kcy = known.y + known.h / 2;
  // Right semicircle for a few peers; full circle when many.
  const full = count > 4;
  const span = full ? Math.PI * 2 : Math.PI;
  const start = -Math.PI / 2;
  const angle =
    count <= 1 ? 0 : start + ((index + 0.5) / count) * span;
  const ring = Math.floor(index / 6);
  const reach = 10000 + ring * (GHOST_H + GHOST_GAP);
  const edge = boxEdgeToward(
    expand,
    kcx + Math.cos(angle) * reach,
    kcy + Math.sin(angle) * reach,
  );
  return {
    x: Math.max(0, edge.x - GHOST_W / 2),
    y: Math.max(0, edge.y - GHOST_H / 2),
  };
}

/** Stable id for an unknown/unmanaged LLDP peer on a site plan. */
function lldpPeerKey(link: LldpLink): string {
  const peer =
    link.b_chassis_id ||
    link.b_label ||
    link.b_port_label ||
    link.b_port ||
    "unknown";
  return `${link.a_device}|${link.a_port}|${peer}`;
}

/**
 * Port pill text, marked when it's an SFP/SFP+ cage rather than copper —
 * the same " · " compound-label convention the backend already uses for a
 * facade group ("1-48 · PoE", cf. internal/portrules), reused here rather
 * than inventing a new visual language for one badge. `dev` undefined
 * (an LLDP neighbor not yet in inventory) or the port unresolved: no claim
 * is made, same as everywhere else a ghost peer's media is unknown.
 */
function portLabelWithMedia(
  dev: Device | undefined,
  port: string | null | undefined,
): string | null {
  if (!port) return null;
  if (dev && isSFPPort(dev, port)) return `${port} · SFP+`;
  return port;
}

const clampZoom = (z: number) => Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, z));

/**
 * THE FLOOR THE "FIT" GESTURE IS ALLOWED TO GO THROUGH.
 *
 * `MIN_ZOOM` is 0.3 because below that a node is unreadable, and a user
 * zooming out by hand should be stopped before the map becomes decoration.
 * Fitting is a different question: on a 393px phone a site of nineteen
 * devices simply does not go into the window at 0.3, and the honest answer
 * to "show me the whole site" is a very small whole site — not a readable
 * corner of it, which is what the clamp produced.
 */
const MIN_FIT_ZOOM = 0.06;

export default function Plan() {
  const { t, i18n } = useTranslation();
  const narrow = useBreakpoint() === "narrow";
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const navigate = useNavigate();
  const role = useSiteRole(siteId);
  const qc = useQueryClient();
  // Link creation: pick source on the canvas, rubber-band to cursor, pick dest,
  // then open the port dialog. Esc or Cancel aborts before the dialog.
  const [linkPick, setLinkPick] = useState<"off" | "source" | "dest">("off");
  const [linkSourceId, setLinkSourceId] = useState<string | null>(null);
  const [linkCursor, setLinkCursor] = useState<{ x: number; y: number } | null>(null);
  const [linkPair, setLinkPair] = useState<{ a: string; b: string } | null>(null);
  const [lldpShow, setLldpShow] = useState(() => {
    try {
      return localStorage.getItem(LLDP_SHOW_KEY) === "1";
    } catch {
      return false;
    }
  });
  const [stpView, setStpView] = useState(false);
  // Declared (hand-entered) link selected on the canvas — engineer can remove it.
  const [selectedLinkId, setSelectedLinkId] = useState<string | null>(null);
  // 24 h bandwidth tip while the pointer rests on a topology link.
  const [linkHover, setLinkHover] = useState<LinkTrafficHover | null>(null);
  const linkTrafficTipId = "topology-link-traffic";
  useEffect(() => {
    return registerHoverTip(linkTrafficTipId, () => setLinkHover(null));
  }, [linkTrafficTipId]);
  // Temporary position while dragging (device id or LLDP peer key → {x,y}).
  const [dragPos, setDragPos] = useState<Record<string, { x: number; y: number }>>({});
  const dragRef = useRef<{
    kind: "device" | "peer";
    id: string;
    dx: number;
    dy: number;
    moved: boolean;
    zoom: number;
  } | null>(null);
  /**
   * ACTUAL height of each node, measured after rendering.
   *
   * A zone frame computed from a guessed height doesn't actually contain
   * what it claims to: as soon as a switch has more ports than average, it
   * overflows its own zone. Since the frame is the only thing saying "this
   * device lives here", an overflow is not a cosmetic glitch — it makes the
   * plan lie.
   */
  const [nodeH, setNodeH] = useState<Record<string, number>>({});
  // ONE observer for all nodes, released when unmounted: create one
  // per node on each render would leave some behind on each move.
  const sizes = useRef<ResizeObserver | null>(null);
  const observed = useRef(new Map<Element, string>());
  if (!sizes.current && typeof ResizeObserver !== "undefined") {
    sizes.current = new ResizeObserver((entries) => {
      setNodeH((prev) => {
        let next = prev;
        entries.forEach((e) => {
          const id = observed.current.get(e.target);
          const h = (e.target as HTMLElement).offsetHeight;
          if (id && next[id] !== h) next = { ...next, [id]: h };
        });
        return next;
      });
    });
  }

  const cancelLinkPick = () => {
    setLinkPick("off");
    setLinkSourceId(null);
    setLinkCursor(null);
  };
  const startLinkPick = () => {
    setSelectedLinkId(null);
    setLinkPair(null);
    setLinkSourceId(null);
    setLinkCursor(null);
    setLinkPick("source");
  };

  const wrapRef = useRef<HTMLDivElement | null>(null);
  // Scroll offset to apply right after a zoom change, so the point of the
  // plan that was under the cursor stays under the cursor.
  const scrollFix = useRef<{ left: number; top: number } | null>(null);
  const [viewport, setViewport] = useState({ w: 0, h: 0 });
  const [zoom, setZoom] = useState(() => {
    const saved = Number(localStorage.getItem(ZOOM_KEY));
    return Number.isFinite(saved) && saved > 0 ? clampZoom(saved) : 1;
  });

  useEffect(() => {
    localStorage.setItem(ZOOM_KEY, String(zoom));
  }, [zoom]);

  useEffect(() => () => sizes.current?.disconnect(), []);

  useEffect(() => {
    if (linkPick === "off" && !selectedLinkId) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      if (linkPick !== "off") cancelLinkPick();
      else setSelectedLinkId(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [linkPick, selectedLinkId]);

  // Rubber-band follows the pointer in plan coordinates (scroll + zoom aware).
  useEffect(() => {
    if (linkPick !== "dest") return;
    const onMove = (e: PointerEvent) => {
      const el = wrapRef.current;
      if (!el) return;
      const rect = el.getBoundingClientRect();
      setLinkCursor({
        x: (el.scrollLeft + (e.clientX - rect.left)) / zoom,
        y: (el.scrollTop + (e.clientY - rect.top)) / zoom,
      });
    };
    window.addEventListener("pointermove", onMove);
    return () => window.removeEventListener("pointermove", onMove);
  }, [linkPick, zoom]);

  useEffect(() => {
    try {
      localStorage.setItem(LLDP_SHOW_KEY, lldpShow ? "1" : "0");
    } catch {
      /* ignore quota / private mode */
    }
  }, [lldpShow]);

  useEffect(() => {
    const measure = () => {
      const el = wrapRef.current;
      if (el) setViewport({ w: el.clientWidth, h: el.clientHeight });
    };
    measure();
    window.addEventListener("resize", measure);
    return () => window.removeEventListener("resize", measure);
  }, []);

  useEffect(() => {
    const el = wrapRef.current;
    const fix = scrollFix.current;
    if (!el || !fix) return;
    scrollFix.current = null;
    el.scrollLeft = Math.max(0, fix.left);
    el.scrollTop = Math.max(0, fix.top);
  }, [zoom]);

  /** Zooms while keeping the targeted point (cursor, or center of the window) fixed. */
  const zoomAround = (next: number, clientX?: number, clientY?: number) => {
    const z = clampZoom(next);
    const el = wrapRef.current;
    if (el && z !== zoom) {
      const rect = el.getBoundingClientRect();
      const cx = (clientX ?? rect.left + rect.width / 2) - rect.left;
      const cy = (clientY ?? rect.top + rect.height / 2) - rect.top;
      const wx = (el.scrollLeft + cx) / zoom;
      const wy = (el.scrollTop + cy) / zoom;
      scrollFix.current = { left: wx * z - cx, top: wy * z - cy };
    }
    setZoom(z);
  };

  // Ctrl/⌘ + wheel: zoom. Non-passive listener, otherwise the browser's own
  // zoom cannot be prevented.
  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      if (!e.ctrlKey && !e.metaKey) return;
      e.preventDefault();
      zoomAround(zoom * (e.deltaY < 0 ? 1.1 : 1 / 1.1), e.clientX, e.clientY);
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [zoom]);

  // Real-time. A meaningful bonus here: the update is not echoed back to
  // the tab that wrote it (see TAB_ID in api.ts), so drag-and-drop does not
  // trigger a reload on every saved move.
  const refetchInterval = useSiteLive(siteId);

  const { data: devices, error } = useQuery({
    queryKey: ["devices", siteId],
    queryFn: () => get<Device[]>(`/api/sites/${siteId}/devices`),
    enabled: !!siteId,
    refetchInterval,
  });
  const { data: vlans } = useQuery({
    queryKey: ["vlans", siteId],
    queryFn: () => get<Vlan[]>(`/api/sites/${siteId}/vlans`),
    enabled: !!siteId,
  });
  const { data: links } = useQuery({
    queryKey: ["links", siteId],
    queryFn: () => get<DeviceLink[]>(`/api/sites/${siteId}/links`),
    enabled: !!siteId,
    refetchInterval,
  });
  /**
   * The OBSERVED LLDP neighborhood, in a query SEPARATE from the one for
   * entered links.
   *
   * Kept separate rather than merged, for the same reason that applies to
   * the inventory: these are two different natures of fact. A hand-entered
   * link is an intention that survives the cable being unplugged; a
   * neighbor is a minute-old observation. Serving them from the same
   * request would have invited them to be displayed the same way — and
   * above all, the absence of an LLDP observation must never erase the
   * declared site plan.
   *
   * `refetchInterval` is the site's, same as for links: the controller does
   * NOT notify neighborhood changes (see handleProxyLLDP, which explains
   * why waking up every browser twice a minute per switch would cost more
   * than it would bring).
   */
  const { data: lldp } = useQuery({
    queryKey: ["lldp", siteId],
    queryFn: () => get<SiteLldp>(`/api/sites/${siteId}/lldp`),
    enabled: !!siteId,
    refetchInterval,
  });
  const { data: plan } = useQuery({
    queryKey: ["plan", siteId],
    queryFn: () => get<SitePlan>(`/api/sites/${siteId}/plan`),
    enabled: !!siteId,
  });
  const vm = vlanMap(vlans);
  const byId = new Map((devices ?? []).map((d) => [d.id, d]));

  /**
   * A SWITCH WITH NOTHING ATTACHED IS A QUESTION, and this is the answer.
   *
   * Measured case (sw01-arena, FS S3400, 2026-10-25): a switch carrying a
   * live 20 Gb/s uplink to the site core sat on this plan connected to
   * nothing, with no indication of why. The backend knew why the whole time
   * — `silent[]` has carried the reason and `silent_help` its vocabulary
   * since the endpoint existed — and `lldpSilencePhrase` had been written to
   * phrase it. Nothing on any screen called it. The explanation existed at
   * both ends of the wire and never reached an operator.
   *
   * The condition is "no edge of EITHER kind", not "no LLDP link": a device
   * cabled in the declared plan is explained by that plan, and badging it
   * because an observation is missing would decorate the normal case. Only a
   * node the operator sees truly alone earns a word.
   */
  const silenceById = new Map(
    (lldp?.silent ?? []).map((s) => [s.device_id, s]),
  );
  const connectedIds = (() => {
    const ids = new Set<string>();
    for (const l of links ?? []) {
      ids.add(l.a_device);
      ids.add(l.b_device);
    }
    for (const l of lldp?.links ?? []) {
      ids.add(l.a_device);
      if (l.b_device) ids.add(l.b_device);
    }
    return ids;
  })();

  const savePos = useMutation({
    mutationFn: (d: Device) =>
      patch<Device>(`/api/devices/${d.id}`, {
        group_id: d.group_id, name: d.name, model: d.model, vendor: d.vendor,
        location: d.location, mgmt_ip: d.mgmt_ip, status: d.status,
        status_note: d.status_note, poe_budget_w: d.poe_budget_w,
        pos_x: d.pos_x, pos_y: d.pos_y,
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["devices", siteId] }),
  });
  const savePeerPos = useMutation({
    mutationFn: async ({ key, x, y }: { key: string; x: number; y: number }) => {
      const cur =
        qc.getQueryData<SitePlan>(["plan", siteId]) ?? plan ?? {};
      const peers = { ...(cur.lldp_peers ?? {}), [key]: { x, y } };
      const next: SitePlan = { ...cur, lldp_peers: peers };
      await put(`/api/sites/${siteId}/plan`, next);
      return next;
    },
    onSuccess: (next, vars) => {
      qc.setQueryData(["plan", siteId], next);
      setDragPos((s) => {
        const copy = { ...s };
        delete copy[vars.key];
        return copy;
      });
    },
  });
  const removeLink = useMutation({
    mutationFn: (id: string) => del(`/api/links/${id}`),
    onSuccess: (_void, id) => {
      setSelectedLinkId((cur) => (cur === id ? null : cur));
      qc.invalidateQueries({ queryKey: ["links", siteId] });
    },
  });

  const posOf = (d: Device) => dragPos[d.id] ?? { x: d.pos_x, y: d.pos_y };

  const deviceBox = (d: Device): PlanBox => {
    const p = posOf(d);
    return { x: p.x, y: p.y, w: NODE_W, h: nodeH[d.id] ?? NODE_H };
  };

  /**
   * Unmanaged / unknown LLDP peers: no inventory device on the far end.
   * Shown as normal equipment boxes when LLDP is on; positions fan around the
   * known switch (clear of its box) unless the user has saved a placement.
   */
  const lldpGhosts = (() => {
    if (!lldpShow) return [] as {
      key: string;
      knownId: string;
      knownPort: string;
      label: string;
      portLabel: string;
      link: LldpLink;
      x: number;
      y: number;
    }[];
    const groups = new Map<string, LldpLink[]>();
    for (const link of lldp?.links ?? []) {
      const known = byId.get(link.a_device);
      if (!known) continue;
      const peerKnown = !!(link.b_device && byId.get(link.b_device));
      if (peerKnown) continue;
      const list = groups.get(link.a_device) ?? [];
      list.push(link);
      groups.set(link.a_device, list);
    }
    const out: {
      key: string;
      knownId: string;
      knownPort: string;
      label: string;
      portLabel: string;
      link: LldpLink;
      x: number;
      y: number;
    }[] = [];
    groups.forEach((list, knownId) => {
      const known = byId.get(knownId);
      if (!known) return;
      const box = deviceBox(known);
      list.forEach((link, i) => {
        const key = lldpPeerKey(link);
        const n = list.length;
        const fallback = defaultLldpPeerPos(box, i, n);
        const saved = plan?.lldp_peers?.[key];
        const pos = dragPos[key] ?? saved ?? fallback;
        const label =
          link.b_label ||
          link.b_chassis_id ||
          t("planPage.unknownNeighbor");
        const portLabel = link.b_port || link.b_port_label || "";
        out.push({
          key,
          knownId,
          knownPort: link.a_port,
          label,
          portLabel,
          link,
          x: pos.x,
          y: pos.y,
        });
      });
    });
    return out;
  })();

  /**
   * Extent of the plan. It FOLLOWS the content (plus a sliding margin) and
   * covers at minimum the visible area: no strip that is visible but off
   * limits for placement, regardless of screen width or zoom.
   */
  const extent = (() => {
    let mx = 0;
    let my = 0;
    (devices ?? []).forEach((d) => {
      const p = posOf(d);
      mx = Math.max(mx, p.x + NODE_W);
      my = Math.max(my, p.y + NODE_H);
    });
    lldpGhosts.forEach((g) => {
      mx = Math.max(mx, g.x + GHOST_W);
      my = Math.max(my, g.y + GHOST_H);
    });
    return { w: mx, h: my };
  })();
  const W = Math.min(
    WORLD_MAX,
    Math.max(plan?.width ?? 1120, extent.w + WORLD_SLACK, viewport.w / zoom),
  );
  const H = Math.min(
    WORLD_MAX,
    Math.max(plan?.height ?? 620, extent.h + WORLD_SLACK, viewport.h / zoom),
  );

  /** Frame the entire site in the window. */
  const fitToView = () => {
    const el = wrapRef.current;
    if (!el || extent.w <= 0 || extent.h <= 0) return;
    const pad = 32;
    scrollFix.current = { left: 0, top: 0 };
    const wanted = Math.min(
      1,
      (el.clientWidth - pad) / extent.w,
      (el.clientHeight - pad) / extent.h,
    );
    setZoom(Math.min(MAX_ZOOM, Math.max(MIN_FIT_ZOOM, wanted)));
  };

  /**
   * ON A PHONE THE MAP OPENS FITTED, once, when the extent is first known.
   *
   * The stored zoom is a DESKTOP preference — it is the same key on both —
   * and restoring 100% on a 393px screen opens the weathermap on a corner of
   * one rack with no clue that there are eighteen more devices off screen.
   * Fitting is what the first glance is for; panning in is the next gesture,
   * and it still works.
   *
   * Once only: a re-fit on every extent change would fight a user who has
   * just zoomed in to read a port.
   */
  const didAutoFit = useRef(false);
  useEffect(() => {
    if (didAutoFit.current || !narrow) return;
    if (extent.w <= 0 || extent.h <= 0) return;
    if (!wrapRef.current || wrapRef.current.clientWidth <= 0) return;
    didAutoFit.current = true;
    fitToView();
  });

  const onPointerDown = (e: React.PointerEvent, d: Device) => {
    // While picking link ends, a click selects — never starts a drag.
    if (linkPick !== "off") return;
    setSelectedLinkId(null);
    const pos = posOf(d);
    dragRef.current = {
      kind: "device",
      id: d.id,
      zoom,
      moved: false,
      dx: e.clientX / zoom - pos.x,
      dy: e.clientY / zoom - pos.y,
    };
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  };
  const onPeerPointerDown = (
    e: React.PointerEvent,
    key: string,
    pos: { x: number; y: number },
  ) => {
    if (linkPick !== "off") return;
    setSelectedLinkId(null);
    dragRef.current = {
      kind: "peer",
      id: key,
      zoom,
      moved: false,
      dx: e.clientX / zoom - pos.x,
      dy: e.clientY / zoom - pos.y,
    };
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  };
  const onPointerMove = (e: React.PointerEvent) => {
    const dr = dragRef.current;
    if (!dr || role === "viewer") return;
    // Pointer coordinates are in screen pixels: we convert back to plan
    // space by dividing by the zoom factor captured at the start of the drag.
    const x = Math.max(0, Math.min(WORLD_MAX - NODE_W, e.clientX / dr.zoom - dr.dx));
    const y = Math.max(0, Math.min(WORLD_MAX - NODE_H, e.clientY / dr.zoom - dr.dy));
    const prev = dragPos[dr.id];
    if (prev && Math.abs(prev.x - x) < 1 && Math.abs(prev.y - y) < 1) return;
    dr.moved = true;
    setDragPos((s) => ({ ...s, [dr.id]: { x, y } }));
  };
  const onPointerUp = (d: Device) => {
    if (linkPick === "source") {
      const pos = posOf(d);
      setLinkSourceId(d.id);
      setLinkCursor({ x: pos.x + NODE_CX, y: pos.y + NODE_CY });
      setLinkPick("dest");
      return;
    }
    if (linkPick === "dest") {
      if (!linkSourceId || d.id === linkSourceId) return;
      setLinkPair({ a: linkSourceId, b: d.id });
      cancelLinkPick();
      return;
    }
    const dr = dragRef.current;
    dragRef.current = null;
    if (!dr || dr.kind !== "device") return;
    if (dr.moved && role !== "viewer") {
      const pos = dragPos[d.id];
      if (pos) savePos.mutate({ ...d, pos_x: pos.x, pos_y: pos.y });
    } else {
      navigate(deviceDetailPath(siteSlug, d));
    }
  };
  const onPeerPointerUp = (key: string) => {
    const dr = dragRef.current;
    dragRef.current = null;
    if (!dr || dr.kind !== "peer" || dr.id !== key) return;
    if (dr.moved && role !== "viewer") {
      const pos = dragPos[key];
      if (pos) savePeerPos.mutate({ key, x: pos.x, y: pos.y });
    }
  };

  const portIntent = (dev: Device | undefined, portName: string) => {
    if (!dev) return null;
    const ports = expandPorts(dev);
    return ports.find((p) => p.name === portName)?.p ?? null;
  };

  const portRefOf = (dev: Device | undefined, portName: string) => {
    if (!dev) return null;
    return expandPorts(dev).find((p) => p.name === portName) ?? null;
  };

  /**
   * The ONE SPEED LABEL printed in the middle of a link — driver/catalog only,
   * never a hand-entered `link.speed`.
   *
   * It no longer carries a capacity in Mb/s, and it must not: ADR-0026 reads
   * each half against ITS OWN port's line rate, so a single number for the
   * whole link would be the thing that erased a 10 G cage facing a 1 G one. See
   * [linkEndEgress]. The LABEL stays single because it is prose for a human and
   * a 1G/10G mismatch is already visible as two differently coloured halves.
   */
  const linkDriverSpeed = (
    a: Device | undefined, aPort: string,
    b: Device | undefined, bPort: string,
  ) => {
    const pa = portRefOf(a, aPort);
    const pb = portRefOf(b, bPort);
    const observed = pa?.p.link || pb?.p.link || null;
    // Prefer the higher facade when ends differ (e.g. 1G↔10G).
    const fa = linkCapacityMbps(null, pa?.speed);
    const fb = linkCapacityMbps(null, pb?.speed);
    const facade = fa >= fb ? (pa?.speed || pb?.speed) : (pb?.speed || pa?.speed);
    return {
      observed: observed || undefined,
      facade,
      label: linkSpeedLabel(observed, facade),
    };
  };

  const stpPortOf = (dev: Device | undefined, portName: string) => {
    const ports = dev?.stp?.ports ?? [];
    return ports.find((p) => p.port === portName);
  };

  const stpEdgeHint = (sp?: { edge?: boolean; edge_mode?: string }) => {
    if (!sp) return "";
    if (sp.edge_mode === "yes") return "edge";
    if (sp.edge_mode === "auto") return "auto";
    if (sp.edge_mode === "no") return "";
    if (sp.edge) return "edge";
    return "";
  };

  const linkOnline = (a: Device | undefined, aPort: string, b: Device | undefined, bPort: string) => {
    if (!a || !b) return false;
    if (a.status === "off" || b.status === "off") return false;
    const pa = portIntent(a, aPort);
    const pb = portIntent(b, bPort);
    // Prefer live link flags; fall back to declared link status when ports unread.
    if (pa?.link === null || pb?.link === null) return false;
    if (pa?.link || pb?.link) return true;
    return true;
  };

  const linkBlocking = (a: Device | undefined, aPort: string, b: Device | undefined, bPort: string) => {
    const blocked = (s: string | undefined) =>
      s === "discarding" || s === "blocking";
    return blocked(stpPortOf(a, aPort)?.state) || blocked(stpPortOf(b, bPort)?.state);
  };

  /**
   * Port rates: computed the same way as in the switch view, so the
   * values match from one screen to another.
   */
  const portRates = (dev: Device | undefined, portName: string) => {
    if (!dev) return null;
    const ports = expandPorts(dev);
    const idx = ports.findIndex((p) => p.name === portName);
    if (idx < 0) return null;
    const p = ports[idx].p;
    const declared = (p.rx_mbps ?? 0) > 0 || (p.tx_mbps ?? 0) > 0;
    const off = dev.status === "off" || !p.link;
    return {
      has: declared,
      rx: off ? 0 : (p.rx_mbps ?? 0),
      tx: off ? 0 : (p.tx_mbps ?? 0),
    };
  };

  /**
   * Speeds of a link in both directions. We read the port of switch A;
   * if it has no counters, take switch B and invert directions
   * (what B receives is what A emits).
   */
  const linkRatesBetween = (
    aDev: Device | undefined, aPort: string,
    bDev: Device | undefined, bPort: string,
  ) => {
    const pa = portRates(aDev, aPort);
    const pb = portRates(bDev, bPort);
    if (pa?.has) return { toA: pa.rx, toB: pa.tx, from: "A" as const };
    if (pb?.has) return { toA: pb.tx, toB: pb.rx, from: "B" as const };
    return { toA: 0, toB: 0, from: null };
  };

  const linkRates = (l: DeviceLink) =>
    linkRatesBetween(byId.get(l.a_device), l.a_port, byId.get(l.b_device), l.b_port);

  /**
   * Is this device's inventory rate on this port a READING, or a zero that only
   * means nobody wrote anything?
   *
   * `internal/api/portrates.go` answers this in its own words: "Zero is not 'no
   * measurement' — inventory cannot express ignorance on this field", and each
   * pass zeroes every faceplate port whose series the store no longer returns.
   * So the field itself cannot be asked, and the question has to be put to the
   * DEVICE instead — which is where the product already has an answer, and the
   * same one three other screens use:
   *
   *   status "off"       unreachable: whatever is in inventory is the last
   *                      thing we saw, not the current rate
   *   neverObserved      monitoring has never spoken about it (UI-GUIDE §2)
   *   blindNow           observed before, no connected proxy can watch it now —
   *                      so its series have stopped and its rates were zeroed
   *   no rate field      no pass has ever touched this port
   *
   * Outside those four, a zero is a real idle port and must be painted as one.
   *
   * THE LIMIT, stated rather than hidden: a single port with no series on a
   * device that is otherwise polled gets a zero like any idle port, and this
   * cannot tell the difference. Fixing that needs the series store, i.e. a
   * telemetry query per device on a page that currently makes none — the hover
   * tip pays for one device at a time, deliberately.
   */
  const egressIsReading = (dev: Device, pr: PortRef) =>
    dev.status !== "off" &&
    !neverObserved(dev) &&
    !blindNow(dev) &&
    (pr.p.rx_mbps != null || pr.p.tx_mbps != null);

  /**
   * THE EGRESS OF ONE END of a link, and the line rate to read it against
   * (ADR-0026).
   *
   * The egress of the switch at this end is the `tx` of ITS port. The `rx` of
   * the far port measures the same quantity minus what was lost on the way, so
   * it serves as the fallback when the near end has no counters — never as an
   * average and never added to it, because the asymmetry between the two
   * directions is the entire reason this half has a colour of its own.
   *
   * The capacity is always the NEAR port's, even when the rate came from the
   * far one: it is the near port that has to carry this egress, and a 10 G cage
   * facing a 1 G one is a fact the two halves are meant to show.
   */
  const linkEndEgress = (
    near: Device | undefined, nearPort: string,
    far: Device | undefined, farPort: string,
  ): { known: boolean; mbps: number; capacityMbps: number } => {
    const pn = portRefOf(near, nearPort);
    const capacityMbps = linkCapacityMbps(pn?.p.link, pn?.speed);
    if (near && pn && egressIsReading(near, pn)) {
      return { known: true, mbps: pn.p.tx_mbps ?? 0, capacityMbps };
    }
    const pf = portRefOf(far, farPort);
    if (far && pf && egressIsReading(far, pf)) {
      return { known: true, mbps: pf.p.rx_mbps ?? 0, capacityMbps };
    }
    return { known: false, mbps: 0, capacityMbps };
  };

  /** Ramp ink for one half, or null when that end has nothing to show. */
  const egressInk = (e: { known: boolean; mbps: number; capacityMbps: number }) =>
    e.known ? trafficColor(e.mbps, e.capacityMbps) : null;

  /**
   * The pill under a half: the glyph points the way that end's traffic LEAVES,
   * so it agrees with the point drawn on the same half, and `?` — the glyph
   * this product already uses for an unknown vendor and an unnamed neighbour —
   * stands in for a rate that was never measured. Never a zero: a zero paints
   * and reads as "at rest", which is a claim nobody made.
   */
  const egressPill = (
    e: { known: boolean; mbps: number },
    glyph: string,
  ) => `${glyph} ${e.known ? fmtMbps(e.mbps) : "?"}`;

  /** Prefer the end that already feeds live rates; fall back to A. */
  const linkHoverSource = (
    key: string,
    a: Device,
    aPort: string,
    bName: string,
    b: Device | undefined,
    bPort: string,
    from: "A" | "B" | null,
    capacity: string | undefined,
    e: React.MouseEvent,
  ): LinkTrafficHover => {
    const side: "A" | "B" =
      from === "B" && b ? "B" : "A";
    return {
      key,
      deviceId: side === "A" ? a.id : b!.id,
      port: side === "A" ? aPort : bPort,
      from: side,
      nameA: a.name,
      nameB: b?.name || bName,
      capacity,
      clientX: e.clientX,
      clientY: e.clientY,
    };
  };
  const onLinkHover = (h: LinkTrafficHover) => {
    claimHoverTip(linkTrafficTipId);
    setLinkHover(h);
  };
  const onLinkHoverMove = (e: React.MouseEvent, key: string) => {
    setLinkHover((cur) =>
      cur && cur.key === key
        ? { ...cur, clientX: e.clientX, clientY: e.clientY }
        : cur,
    );
  };
  const onLinkHoverLeave = (key: string) => {
    setLinkHover((cur) => {
      if (cur?.key !== key) return cur;
      releaseHoverTip(linkTrafficTipId);
      return null;
    });
  };

  const diagnostics = (links ?? []).filter((l) => l.status !== "up" || l.note);
  const detail = zoom >= LOD_ZOOM;

  /**
   * The dotted frames are DERIVED from the equipment: one zone per
   * location, drawn around its switches. Moving a switch therefore
   * distorts its frame, and changing its location makes it change zone —
   * no geometry to maintain by hand.
   */
  const measure = (id: string) => (el: HTMLDivElement | null) => {
    if (!el) return;
    observed.current.set(el, id);
    sizes.current?.observe(el);
  };

  const zones = (() => {
    const byLocation = new Map<string, Device[]>();
    (devices ?? []).forEach((d) => {
      const key = (d.location || "").trim();
      if (!key) return;
      const list = byLocation.get(key);
      if (list) list.push(d);
      else byLocation.set(key, [d]);
    });
    return [...byLocation].map(([label, members]) => {
      const xs = members.map((d) => posOf(d).x);
      const ys = members.map((d) => posOf(d).y);
      // The bottom of the frame follows the LOWEST-reaching node, actual
      // height included — not the node placed lowest on screen, which
      // might be the shortest one.
      const bottoms = members.map((d) => posOf(d).y + (nodeH[d.id] ?? NODE_H));
      const left = Math.min(...xs) - ZONE_PAD;
      const top = Math.min(...ys) - ZONE_PAD_TOP;
      return {
        label,
        count: members.length,
        x: left,
        y: top,
        w: Math.max(...xs) + NODE_W + ZONE_PAD - left,
        h: Math.max(...bottoms) + ZONE_PAD - top,
      };
    });
  })();

  return (
    <div className="plan-page">
      {/* No page heading: the tab bar names this page. The equipment count
          stays — it is an observation, not a label — and takes the slot the
          title had, which keeps the view controls at the end of the toolbar
          where they were. */}
      <div className="toolbar">
        <span className="note">
          {t("planPage.subtitle", { count: (devices ?? []).length })}
          {/* The two halves are the one thing on this map nobody can derive by
              looking: that the colour is the EGRESS of the switch on that side,
              and that a grey half with a `?` is a reading we do not have rather
              than an idle wire (ADR-0026). Behind a HelpTip because it is an
              explanation, not an observation — UI-GUIDE writing style. */}
          <HelpTip label={t("planPage.linkRenderHelpLabel")}>
            <p>{t("planPage.linkRenderHelpHalves")}</p>
            <p>{t("planPage.linkRenderHelpRamp")}</p>
            <p>{t("planPage.linkRenderHelpUnknown")}</p>
            <p>{t("planPage.linkRenderHelpBlocked")}</p>
          </HelpTip>
        </span>
        <div className="row" style={{ gap: 6 }}>
          <div className="zoom-ctl">
            <QuickTip text={t("planPage.zoomOut")} className="quicktip-inline">
              <button
                className="secondary icon-btn"
                onClick={() => zoomAround(zoom / ZOOM_STEP)}
                disabled={zoom <= MIN_ZOOM}
                aria-label={t("planPage.zoomOut")}
              >
                −
              </button>
            </QuickTip>
            <span className="zoom-level">{Math.round(zoom * 100)} %</span>
            <QuickTip text={t("planPage.zoomIn")} className="quicktip-inline">
              <button
                className="secondary icon-btn"
                onClick={() => zoomAround(zoom * ZOOM_STEP)}
                disabled={zoom >= MAX_ZOOM}
                aria-label={t("planPage.zoomIn")}
              >
                +
              </button>
            </QuickTip>
          </div>
          <QuickTip text={t("planPage.fitView")} className="quicktip-inline">
            <button className="secondary" onClick={fitToView}>
              {t("planPage.fitView")}
            </button>
          </QuickTip>
          <button className="secondary" onClick={() => zoomAround(1)} disabled={zoom === 1}>
            100 %
          </button>
          <QuickTip text={t("planPage.stpViewTitle")} className="quicktip-inline">
            <div className={"plan-toggle-btn" + (stpView ? " on" : "")}>
              <Switch
                checked={stpView}
                onChange={setStpView}
                label={t("planPage.stpView")}
              />
            </div>
          </QuickTip>
          <QuickTip text={t("planPage.lldpShowTitle")} className="quicktip-inline">
            <div className={"plan-toggle-btn" + (lldpShow ? " on" : "")}>
              <Switch
                checked={lldpShow}
                onChange={setLldpShow}
                label={t("planPage.lldpShow")}
              />
            </div>
          </QuickTip>
          {role === "ing" && (
            linkPick === "off" ? (
              <ActionButton kind="link" className="secondary" onClick={startLinkPick}>
                {t("planPage.addLink")}
              </ActionButton>
            ) : (
              <>
                <span className="plan-link-hint">
                  {linkPick === "source"
                    ? t("planPage.pickSource")
                    : t("planPage.pickDest")}
                </span>
                <ActionButton kind="cancel" className="secondary" onClick={cancelLinkPick}>
                  {t("common.cancel")}
                </ActionButton>
              </>
            )
          )}
          {role === "ing" && selectedLinkId && linkPick === "off" && (() => {
            const sel = (links ?? []).find((l) => l.id === selectedLinkId);
            if (!sel) return null;
            const aName = byId.get(sel.a_device)?.name ?? "?";
            const bName = byId.get(sel.b_device)?.name ?? "?";
            return (
              <span className="plan-link-selected row" style={{ gap: 8, alignItems: "center" }}>
                <QuickTip
                  text={`${aName} (${sel.a_port}) ↔ ${bName} (${sel.b_port})`}
                  className="quicktip-inline"
                >
                  <span className="plan-link-hint">
                    {t("planPage.selectedLink", {
                      a: aName,
                      b: bName,
                    })}
                  </span>
                </QuickTip>
                <DeleteButton
                  label={t("planPage.removeLink")}
                  onConfirm={() => removeLink.mutate(sel.id)}
                />
                <ActionButton
                  kind="cancel"
                  className="ghost"
                  onClick={() => setSelectedLinkId(null)}
                >
                  {t("common.cancel")}
                </ActionButton>
              </span>
            );
          })()}
        </div>
      </div>
      <ErrorMsg error={error} />

      {diagnostics.length > 0 && (
        <div className="plan-diagnostics">
          <div className="kicker" style={{ marginBottom: 6 }}>{t("planPage.linkDiagnostics")}</div>
          <div className="alert-list">
            {diagnostics.map((l) => {
              const a = byId.get(l.a_device)?.name ?? "?";
              const b = byId.get(l.b_device)?.name ?? "?";
              return (
                <div key={l.id} className="alert-row" style={{ cursor: "default" }}>
                  <span className="dot" style={{ background: linkDiagColor(l.status) }} />
                  <span style={{ fontSize: 12, lineHeight: 1.5 }}>
                    {a} ({l.a_port}) ↔ {b} ({l.b_port})
                    {l.status === "down" ? t("planPage.linkDown") : ""}
                    {l.note ? " — " + l.note : ""}
                  </span>
                  {role === "ing" && (
                    <span style={{ marginLeft: "auto" }}>
                      <DeleteButton onConfirm={() => removeLink.mutate(l.id)} />
                    </span>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      )}

      <div className="plan-body">
        <div className="plan-wrap" ref={wrapRef}>
        {/* Scale is on the canvas; outer layer carries zoomed size so scrolling follows. */}
        <div className="plan-stage" style={{ width: W * zoom, height: H * zoom }}>
        <div
          className={"plan-canvas" + (linkPick !== "off" ? " linking" : "")}
          style={{ width: W, height: H, transform: `scale(${zoom})`, transformOrigin: "0 0" }}
        >
          {zones.map((z) => (
            <div
              key={z.label}
              className="plan-zone"
              style={{ left: z.x, top: z.y, width: z.w, height: z.h }}
            >
              <span>
                {z.label}
                {z.count > 1 && <b> · {z.count}</b>}
              </span>
            </div>
          ))}

          {/* Links share node coordinate space: no SVG scaling or strokes would shift. */}
          <svg
            width={W}
            height={H}
            className="plan-links"
            onClick={() => {
              if (selectedLinkId) setSelectedLinkId(null);
            }}
          >
            <defs>
              {/* The glow that MAKES an offline link readable — the near-black
                  stroke it sits on carries no signal by itself
                  (topologyLinks.ts). It was #e5484d, i.e. --danger written out.
                  var() DOES resolve in an SVG presentation attribute — measured
                  on Chromium 124 and Firefox 152, both of which paint this
                  filter rgb(229,73,77) — which is also what the rest of the
                  frontend already relies on (viz.tsx's stroke="var(--border)",
                  TopologyLinkLabels' fill="var(--bg-panel)"). No CSS rule
                  needed; see UI-GUIDE §6. */}
              <filter id="topo-link-offline" x="-40%" y="-40%" width="180%" height="180%">
                <feDropShadow dx="0" dy="0" stdDeviation="2.2" floodColor="var(--danger)" floodOpacity="0.95" />
              </filter>
            </defs>
            {/* OBSERVED LLDP links, UNDER declared links.
                Known↔known (managed): always drawn, offset beside declared.
                Unknown/unmanaged peers: only when Show LLDP is on (ghost boxes). */}
            {(lldp?.links ?? []).map((l) => {
              const a = byId.get(l.a_device);
              if (!a) return null;
              const b = l.b_device ? byId.get(l.b_device) : undefined;
              // Managed↔managed always; ghosts only with the LLDP toggle.
              if (!b && !lldpShow) return null;
              const ghost = !b
                ? lldpGhosts.find((g) => g.key === lldpPeerKey(l))
                : undefined;
              if (!b && !ghost) return null;
              const boxA = deviceBox(a);
              const boxB = b
                ? deviceBox(b)
                : { x: ghost!.x, y: ghost!.y, w: GHOST_W, h: GHOST_H };
              const seg = linkBetweenBoxes(boxA, boxB);
              const dx = seg.x2 - seg.x1, dy = seg.y2 - seg.y1;
              const len = Math.hypot(dx, dy) || 1;
              const ox = b ? (-dy / len) * LLDP_OFFSET : 0;
              const oy = b ? (dx / len) * LLDP_OFFSET : 0;
              const lx1 = seg.x1 + ox, ly1 = seg.y1 + oy;
              const lx2 = seg.x2 + ox, ly2 = seg.y2 + oy;
              const online = b
                ? linkOnline(a, l.a_port, b, l.b_port ?? "")
                : a.status !== "off";
              const blocking = b
                ? linkBlocking(a, l.a_port, b, l.b_port ?? "")
                : false;
              const drv = linkDriverSpeed(a, l.a_port, b, l.b_port ?? "");
              const r = linkRatesBetween(a, l.a_port, b, l.b_port ?? "");
              const isGhost = !b;
              const egA = linkEndEgress(a, l.a_port, b, l.b_port ?? "");
              const egB = linkEndEgress(b, l.b_port ?? "", a, l.a_port);
              const ghostSt = isGhost ? ghostPeerLinkStroke(online) : null;
              const paint: TopologyLinkPaint = ghostSt
                ? {
                    // A ghost peer is one box meshcraft invented for a
                    // neighbour it does not manage: there is no far port and no
                    // far egress, so it keeps its single thin stroke rather
                    // than being split into a half nobody can colour.
                    strokeWidth: ghostSt.strokeWidth,
                    strokeDasharray: ghostSt.strokeDasharray,
                    opacity: ghostSt.opacity,
                    filter: ghostSt.filter,
                    inkA: ghostSt.stroke,
                    inkB: ghostSt.stroke,
                    heads: false,
                  }
                : topologyLinkPaint({
                    online,
                    blocking,
                    loadInkA: egressInk(egA),
                    loadInkB: egressInk(egB),
                    uncertain: lldpLinkKind(l) === "one_side",
                    lldp: true,
                  });
              // Glyphs point the way each end's traffic LEAVES, which is also
              // the way that half's point faces (ADR-0026).
              const outOfA = lx1 <= lx2 ? "▸" : "◂";
              const outOfB = lx1 <= lx2 ? "◂" : "▸";
              const stpA = stpPortOf(a, l.a_port);
              const stpB = b ? stpPortOf(b, l.b_port ?? "") : undefined;
              const midLabel = !online
                ? "DOWN"
                : stpView
                  ? [
                      stpA?.role || stpB?.role || "",
                      (stpA?.path_cost || stpB?.path_cost)
                        ? `cost ${stpA?.path_cost || stpB?.path_cost}`
                        : "",
                      stpEdgeHint(stpA) || stpEdgeHint(stpB),
                    ].filter(Boolean).join(" · ") || drv.label
                  : drv.label;
              const peerPort = l.b_port || l.b_port_label || ghost?.portLabel || "";
              const peerName = b?.name || ghost?.label || "?";
              const hoverKey = `lldp-${l.a_device}-${l.a_port}-${l.b_device ?? ghost?.key}-${l.b_port ?? ""}`;
              const speedText =
                midLabel + (!isGhost && lldpLinkKind(l) === "one_side" ? " · ½" : "");
              const stpExtra =
                stpView && online && b
                  ? [stpA?.state, stpB?.state].filter(Boolean).join(" / ")
                  : "";
              return (
                <g
                  key={hoverKey}
                  className="plan-link-hit"
                  onMouseEnter={(e) =>
                    onLinkHover(
                      linkHoverSource(
                        hoverKey,
                        a,
                        l.a_port,
                        peerName,
                        b,
                        l.b_port ?? "",
                        r.from,
                        drv.label,
                        e,
                      ),
                    )
                  }
                  onMouseMove={(e) => onLinkHoverMove(e, hoverKey)}
                  onMouseLeave={() => onLinkHoverLeave(hoverKey)}
                >
                  {/* Wide invisible stroke for easier hover. */}
                  <line
                    x1={lx1} y1={ly1} x2={lx2} y2={ly2}
                    stroke="transparent" strokeWidth={14}
                  />
                  <LinkBody x1={lx1} y1={ly1} x2={lx2} y2={ly2} paint={paint} />
                  <TopologyLinkLabels
                    x1={lx1}
                    y1={ly1}
                    x2={lx2}
                    y2={ly2}
                    portA={portLabelWithMedia(a, l.a_port)}
                    portB={portLabelWithMedia(b, peerPort)}
                    speedText={speedText}
                    speedFill={!online ? paint.inkA : "var(--text)"}
                    speedLetterSpacing={1}
                    bwA={!stpView && online && !isGhost ? egressPill(egA, outOfA) : null}
                    bwB={!stpView && online && !isGhost ? egressPill(egB, outOfB) : null}
                    extraText={stpExtra || null}
                  />
                </g>
              );
            })}

            {(links ?? []).map((l) => {
              const a = byId.get(l.a_device);
              const b = byId.get(l.b_device);
              if (!a || !b) return null;
              const seg = linkBetweenBoxes(deviceBox(a), deviceBox(b));
              const x1 = seg.x1, y1 = seg.y1, x2 = seg.x2, y2 = seg.y2;
              const r = linkRates(l);
              const online = linkOnline(a, l.a_port, b, l.b_port) && l.status !== "down";
              const blocking = linkBlocking(a, l.a_port, b, l.b_port);
              const drv = linkDriverSpeed(a, l.a_port, b, l.b_port);
              const egA = linkEndEgress(a, l.a_port, b, l.b_port);
              const egB = linkEndEgress(b, l.b_port, a, l.a_port);
              const st = topologyLinkPaint({
                online,
                blocking,
                loadInkA: egressInk(egA),
                loadInkB: egressInk(egB),
              });
              // Glyphs point the way each end's traffic LEAVES (ADR-0026).
              const outOfA = x1 <= x2 ? "▸" : "◂";
              const outOfB = x1 <= x2 ? "◂" : "▸";
              const stpA = stpPortOf(a, l.a_port);
              const stpB = stpPortOf(b, l.b_port);
              const midLabel = !online
                ? "DOWN"
                : stpView
                  ? [
                      stpA?.role || stpB?.role || "",
                      (stpA?.path_cost || stpB?.path_cost)
                        ? `cost ${stpA?.path_cost || stpB?.path_cost}`
                        : "",
                      stpEdgeHint(stpA) || stpEdgeHint(stpB),
                    ].filter(Boolean).join(" · ") || drv.label
                  : drv.label;
              const selected = selectedLinkId === l.id;
              const midFill =
                selected
                  ? "var(--accent)"
                  : !online || l.note
                    ? st.inkA
                    : "var(--text)";
              // Selection repaints both halves and thickens them; the points
              // stay, so a selected forwarding link is still not mistakable for
              // a blocked one (which has none).
              const paint: TopologyLinkPaint = selected
                ? {
                    ...st,
                    strokeWidth: st.strokeWidth + 1.5,
                    filter: undefined,
                    inkA: "var(--accent)",
                    inkB: "var(--accent)",
                  }
                : st;
              const stpExtra =
                stpView && online
                  ? [stpA?.state, stpB?.state].filter(Boolean).join(" / ")
                  : "";
              return (
                <g
                  key={l.id}
                  className={"plan-link-hit" + (selected ? " selected" : "")}
                  style={{ cursor: role === "ing" && linkPick === "off" ? "pointer" : undefined }}
                  onClick={(e) => {
                    if (role !== "ing" || linkPick !== "off") return;
                    e.stopPropagation();
                    setSelectedLinkId((cur) => (cur === l.id ? null : l.id));
                  }}
                  onMouseEnter={(e) =>
                    onLinkHover(
                      linkHoverSource(
                        l.id,
                        a,
                        l.a_port,
                        b.name,
                        b,
                        l.b_port,
                        r.from,
                        drv.label,
                        e,
                      ),
                    )
                  }
                  onMouseMove={(e) => onLinkHoverMove(e, l.id)}
                  onMouseLeave={() => onLinkHoverLeave(l.id)}
                >
                  {/* Wide invisible stroke for easier picking. */}
                  <line
                    x1={x1} y1={y1} x2={x2} y2={y2}
                    stroke="transparent" strokeWidth={14}
                  />
                  <LinkBody x1={x1} y1={y1} x2={x2} y2={y2} paint={paint} />
                  <TopologyLinkLabels
                    x1={x1}
                    y1={y1}
                    x2={x2}
                    y2={y2}
                    portA={portLabelWithMedia(a, l.a_port)}
                    portB={portLabelWithMedia(b, l.b_port)}
                    speedText={midLabel + (l.note && online ? " ⚠" : "")}
                    speedFill={midFill}
                    speedLetterSpacing={1}
                    bwA={!stpView && online ? egressPill(egA, outOfA) : null}
                    bwB={!stpView && online ? egressPill(egB, outOfB) : null}
                    extraText={stpExtra || null}
                  />
                </g>
              );
            })}

            {linkPick === "dest" && linkSourceId && linkCursor && (() => {
              const src = byId.get(linkSourceId);
              if (!src) return null;
              const box = deviceBox(src);
              const edge = boxEdgeToward(box, linkCursor.x, linkCursor.y);
              return (
                <line
                  key="link-draft"
                  x1={edge.x} y1={edge.y}
                  x2={linkCursor.x} y2={linkCursor.y}
                  stroke="var(--accent)" strokeWidth={2}
                  strokeDasharray="6 4" opacity={0.9}
                  pointerEvents="none"
                />
              );
            })()}
          </svg>

          {(devices ?? []).map((d) => {
            const pos = posOf(d);
            const off = d.status === "off";
            // Copper only earns its square once configured; the SFP/SFP+
            // cages are drawn UNCONDITIONALLY — they are the uplinks, and an
            // uplink is exactly what a topology reader looks for. The intent
            // filter used to hide them twice over: unconfigured (libre) like
            // any idle copper port, and configured as the trunk-all a cage
            // almost always carries.
            const devPorts = expandPorts(d).filter(
              (pr) =>
                isSFPRef(pr) ||
                (pr.p.mode && pr.p.mode !== "libre" && pr.p.tagged !== "all"),
            );
            const configured = devPorts.filter((pr) => pr.p.mode && pr.p.mode !== "libre").length;
            const up = expandPorts(d).filter((pr) => pr.p.link && pr.p.mode !== "libre").length;
            // Null whenever there is nothing to say — a device with links, or
            // one whose neighborhood is known and simply empty.
            const isolation = lldpIsolationBadge(silenceById.get(d.id));
            return (
              <div
                key={d.id}
                ref={measure(d.id)}
                className={
                  "plan-node" +
                  (linkSourceId === d.id ? " plan-node-link-src" : "") +
                  (linkPick === "dest" && linkSourceId !== d.id ? " plan-node-link-tgt" : "")
                }
                style={{ left: pos.x, top: pos.y, width: NODE_W }}
                onPointerDown={(e) => onPointerDown(e, d)}
                onPointerMove={onPointerMove}
                onPointerUp={() => onPointerUp(d)}
              >
                <QuickTip
                  text={`${d.name} — ${d.model}${d.location ? " · " + d.location : ""}\n${t("planPage.configuredPorts", { configured, active: up })}`}
                  className="quicktip-block"
                >
                <div className="row" style={{ gap: 7 }}>
                  <StatusDot dev={d} />
                  <span style={{ fontWeight: 700, fontSize: 12, whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>
                    {d.name}
                  </span>
                  {stpView && d.stp?.is_root && (
                    <QuickTip text={t("planPage.stpRoot")} className="quicktip-inline">
                      <span className="plan-stp-root">ROOT</span>
                    </QuickTip>
                  )}
                  {!connectedIds.has(d.id) && isolation && (
                    /* QuickTip and not HelpTip: a plan node is already a
                       click target for selection and dragging, and a second
                       click meaning "explain" would fight the drag. The full
                       paragraph rides the hover. */
                    <QuickTip
                      text={lldpSilencePhrase(silenceById.get(d.id)) || undefined}
                      className="quicktip-inline"
                    >
                      <span
                        className={"plan-node-isolated tone-" + isolation.tone}
                      >
                        {isolation.label}
                      </span>
                    </QuickTip>
                  )}
                </div>
                {stpView && (
                  <QuickTip text={d.stp?.notes?.join(" · ") || undefined} className="quicktip-block">
                  <div className="plan-node-stp">
                    {!d.stp ? (
                      <span className="note">{t("planPage.stpUnknown")}</span>
                    ) : !d.stp.enabled ? (
                      <span className="note">{t("planPage.stpOff")}</span>
                    ) : (
                      <>
                        <span>{(d.stp.mode || "stp").toUpperCase()}</span>
                        {d.stp.bridge_priority != null && d.stp.bridge_priority > 0 && (
                          <QuickTip text={t("planPage.stpDeviceWeight")} className="quicktip-inline">
                            <span>
                              {t("planPage.stpPrio", { value: d.stp.bridge_priority })}
                            </span>
                          </QuickTip>
                        )}
                        {d.stp.is_root ? (
                          <span>{t("planPage.stpIsRoot")}</span>
                        ) : (
                          <>
                            {d.stp.root_path_cost != null && (
                              <QuickTip text={t("planPage.stpRootCost")} className="quicktip-inline">
                                <span>
                                  {t("planPage.stpCost", { value: d.stp.root_path_cost })}
                                </span>
                              </QuickTip>
                            )}
                            {d.stp.root_bridge_id && (
                              <QuickTip text={d.stp.root_bridge_id} className="quicktip-inline">
                                <span className="note">
                                  {t("planPage.stpRootId", {
                                    id: d.stp.root_bridge_id.length > 18
                                      ? d.stp.root_bridge_id.slice(0, 16) + "…"
                                      : d.stp.root_bridge_id,
                                  })}
                                </span>
                              </QuickTip>
                            )}
                          </>
                        )}
                      </>
                    )}
                  </div>
                  </QuickTip>
                )}
                {detail && !stpView && (
                  <>
                    <QuickTip text={d.model || undefined} className="quicktip-block">
                      <div
                        className="note"
                        style={{ fontSize: 9, margin: "1px 0 6px 15px" }}
                      >
                        {modelShort(modelOf(d), d.vendor)}
                      </div>
                    </QuickTip>
                    <div style={{ display: "flex", flexWrap: "wrap", gap: 2, marginLeft: 15 }}>
                      {devPorts.map((pr) => (
                        <PortSquare key={pr.name} name={pr.name} displayName={pr.display} port={pr.p} vm={vm} size={9} off={off} sfp={isSFPRef(pr)} />
                      ))}
                    </div>
                    <div className="note" style={{ fontSize: 9, margin: "6px 0 0 15px" }}>
                      {t("planPage.configuredPorts", { configured: devPorts.length, active: up })}
                    </div>
                  </>
                )}
                {detail && stpView && (
                  <div className="note" style={{ fontSize: 9, margin: "6px 0 0 15px" }}>
                    {t("planPage.configuredPorts", { configured: devPorts.length, active: up })}
                  </div>
                )}
                </QuickTip>
              </div>
            );
          })}

          {lldpShow &&
            lldpGhosts.map((g) => (
              <div
                key={g.key}
                className="plan-node plan-node-ghost"
                style={{ left: g.x, top: g.y, width: GHOST_W }}
                onPointerDown={(e) => onPeerPointerDown(e, g.key, { x: g.x, y: g.y })}
                onPointerMove={onPointerMove}
                onPointerUp={() => onPeerPointerUp(g.key)}
              >
                <QuickTip text={lldpLinkTooltip(g.link, byId)} className="quicktip-block">
                <div className="row" style={{ gap: 7 }}>
                  <span className="plan-ghost-dot" aria-hidden />
                  <span
                    style={{
                      fontWeight: 700,
                      fontSize: 12,
                      whiteSpace: "nowrap",
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                    }}
                  >
                    {g.label}
                  </span>
                </div>
                <div className="note" style={{ fontSize: 10, marginTop: 2 }}>
                  {g.link.b_device
                    ? t("planPage.unmanagedEquipment")
                    : t("planPage.unknownEquipment")}
                  {g.portLabel ? ` · ${g.portLabel}` : ""}
                </div>
                </QuickTip>
              </div>
            ))}
        </div>
        </div>
        </div>
      </div>

      {linkPair && (
        <AddLinkModal
          siteId={siteId!}
          devices={devices ?? []}
          initialA={linkPair.a}
          initialB={linkPair.b}
          onClose={() => setLinkPair(null)}
          onDone={() => {
            setLinkPair(null);
            qc.invalidateQueries({ queryKey: ["links", siteId] });
          }}
        />
      )}
      <LinkTrafficTip hover={linkHover} />
    </div>
  );
}

function AddLinkModal({
  siteId,
  devices,
  initialA,
  initialB,
  onClose,
  onDone,
}: {
  siteId: string;
  devices: Device[];
  initialA: string;
  initialB: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const [a] = useState(initialA);
  const [aPort, setAPort] = useState("");
  const [b] = useState(initialB);
  const [bPort, setBPort] = useState("");
  const [note, setNote] = useState("");

  const create = useMutation({
    mutationFn: () =>
      post(`/api/sites/${siteId}/links`, {
        a_device: a, a_port: aPort, b_device: b, b_port: bPort,
        // Capacity is observed from the driver on the topology — not declared here.
        speed: "", status: "up", note,
      }),
    onSuccess: onDone,
  });

  const portOptions = (devId: string) => {
    const d = devices.find((x) => x.id === devId);
    return d ? expandPorts(d).map((pr) => pr.name) : [];
  };

  const deviceName = (devId: string) =>
    devices.find((x) => x.id === devId)?.name ?? "?";

  const endpoint = (
    label: string,
    dev: string,
    port: string, setPort: (v: string) => void,
  ) => (
    <div className="row">
      <div style={{ flex: 2 }}>
        <Field label={label}>
          <input value={deviceName(dev)} disabled readOnly />
        </Field>
      </div>
      <div style={{ flex: 1 }}>
        <Field label={t("planPage.port")}>
          <select value={port} onChange={(e) => setPort(e.target.value)}>
            <option value="">—</option>
            {portOptions(dev).map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        </Field>
      </div>
    </div>
  );

  return (
    <Modal title={t("planPage.addLinkTitle")} onClose={onClose}>
      {endpoint(t("planPage.endpointA"), a, aPort, setAPort)}
      {endpoint(t("planPage.endpointB"), b, bPort, setBPort)}
      <Field label={t("planPage.note")}>
        <input value={note} onChange={(e) => setNote(e.target.value)} />
      </Field>
      <p className="note" style={{ marginTop: 4 }}>
        {t("planPage.linkCapacityNote")}
      </p>
      <ErrorMsg error={create.error} />
      <div className="actions">
        <ActionButton kind="cancel" className="secondary" onClick={onClose}>
          {t("common.cancel")}
        </ActionButton>
        <ActionButton
          kind="link"
          disabled={!aPort || !bPort || a === b || create.isPending}
          onClick={() => create.mutate()}
        >
          {t("planPage.createLink")}
        </ActionButton>
      </div>
    </Modal>
  );
}


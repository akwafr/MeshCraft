import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { sitePath } from "../paths";
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
import { ActionButton, DeleteButton, ErrorMsg, Field, Modal, QuickTip, Switch } from "../components";
import { expandPorts, fmtMbps, vlanMap } from "../network";
import { modelShort } from "../modelLabel";
import {
  lldpLinkKind,
  lldpLinkTooltip,
} from "../lldp";
import { PortSquare, StatusDot } from "../netui";
import { ghostPeerLinkStroke, linkCapacityMbps, linkSpeedLabel, topologyLinkStroke } from "../topologyLinks";
import { TopologyLinkLabels } from "../TopologyLinkLabels";
import { LinkTrafficTip, type LinkTrafficHover } from "../LinkTrafficTip";
import { claimHoverTip, registerHoverTip, releaseHoverTip } from "../hoverExclusive";

type PlanBox = { x: number; y: number; w: number; h: number };

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

const linkDiagColor = (status: DeviceLink["status"]) =>
  status === "down" ? "#0a0a0a" : status === "warn" ? "#f5a524" : "#46a758";

const NODE_W = 176;
const NODE_CX = 88;
const NODE_CY = 38;
// DEFAULT height of a node, by the time it is rendered and measured. A switch
// loaded with ports largely exceeds this value: being satisfied with this would bring out
// the equipment of the framework of its place. The actual height is measured (nodeH).
const NODE_H = 104;
const ZONE_PAD = 16;
const ZONE_PAD_TOP = 26;

// LLDP_OFFSET: by how much an OBSERVED link shifts from the entered link that it doubles.
//
// The two join the same box edges: without offset, an observation which confirms a
// statement would be superimposed to the pixel, and one of the two would disappear.
// Five pixels are enough to distinguish them without the line appearing to join
// two other pieces of equipment.
const LLDP_OFFSET = 5;

// Zoom: essential when a site has a lot of equipment.
const ZOOM_KEY = "meshcraft.planZoom";
const MIN_ZOOM = 0.3;
const MAX_ZOOM = 2;
const ZOOM_STEP = 1.2;
// Below this factor the nodes switch to compact view: the boxes of
// port are no longer readable, you might as well not draw them (and not pay the
// rendu de plusieurs milliers d'entre elles).
const LOD_ZOOM = 0.6;
// Sliding margin still available beyond the rightmost switch /
// the lowest: the plane therefore never has an impassable edge.
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

const clampZoom = (z: number) => Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, z));

export default function Plan() {
  const { t, i18n } = useTranslation();
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
   * A place frame calculated on a guessed height does not contain what it
   * claims to contain: as soon as a switch has more ports than average, it
   * overflows from its own place. The frame being the only thing that says “these
   * equipment is there”, an overflow is not a cosmetic defect — it
   * fait mentir le plan.
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
  // Scrolling to apply just after a zoom change, to keep under
  // the cursor the point of the plan which was there.
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

  // Ctrl/⌘ + wheel: zoom. Non-passive listener, otherwise impossible to prevent
  // le zoom du navigateur.
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

  // Real time. Significant bonus here: the flow is not sent back to
  // the tab that writes (see TAB_ID in api.ts), so drag and drop does not
  // does not trigger a reload per saved move.
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
   * The LLDP neighborhood OBSERVED, query SEPARATE from that of the links entered.
   *
   * Separated and not merged, for the reason which also applies to the base: they are two
   * natures of fact. A seized connection is an intention that survives unplugging;
   * a neighbor is a minute-old observation. Serve them with the same request
   * would have invited them to display the same — and above all, the absence of findings should not
   * never remove the declared site plan.
   *
   * `refetchInterval` is that of the site, as for links: the controller does not
   * notifie PAS les changements de voisinage (cf. handleProxyLLDP, qui explique
   * why wake up all browsers twice per minute and per switch would have
   * cost more than it brings).
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
   * Scope of the plan. It FOLLOWS the content (plus a sliding margin) and
   * couvre au minimum la zone visible : aucune bande visible mais interdite au
   * deposit, regardless of screen width or zoom.
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
    setZoom(
      clampZoom(
        Math.min(
          1,
          (el.clientWidth - pad) / extent.w,
          (el.clientHeight - pad) / extent.h,
        ),
      ),
    );
  };

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
    // The pointer coordinates are in screen pixels: we return to space
    // of the plan by dividing by the zoom factor of the start of the drag.
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
      navigate(sitePath(siteSlug, "equipment", d.name));
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

  /** Negotiated + facade speeds from both ends — driver/catalog only. */
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
      mbps: linkCapacityMbps(observed, facade),
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
   * Rates of a port: same seeds as the switch view, so that the values
   * match from one screen to another.
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
   * location, drawn around its switches. Moving a distorted switch
   * donc son cadre, et changer son emplacement le fait changer de zone —
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
      // The bottom of the frame follows the LOWEST descending node, actual height
      // understood — and not the lowest knot placed, which may be the shortest.
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
      <div className="toolbar">
        <h1>
          {t("planPage.title")}
          <span className="sub">{t("planPage.subtitle", { count: (devices ?? []).length })}</span>
        </h1>
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
              <filter id="topo-link-offline" x="-40%" y="-40%" width="180%" height="180%">
                <feDropShadow dx="0" dy="0" stdDeviation="2.2" floodColor="#e5484d" floodOpacity="0.95" />
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
              const mbps = Math.max(r.toA, r.toB);
              const isGhost = !b;
              const st = isGhost
                ? ghostPeerLinkStroke(online)
                : topologyLinkStroke({
                    online,
                    blocking,
                    util: drv.mbps > 0 ? mbps / drv.mbps : 0,
                    uncertain: lldpLinkKind(l) === "one_side",
                    lldp: true,
                  });
              const towardA = lx1 <= lx2 ? "◂" : "▸";
              const towardB = lx1 <= lx2 ? "▸" : "◂";
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
                  <line
                    x1={lx1} y1={ly1} x2={lx2} y2={ly2}
                    stroke={st.stroke} strokeWidth={st.strokeWidth}
                    strokeDasharray={st.strokeDasharray} opacity={st.opacity}
                    filter={st.filter}
                  />
                  <TopologyLinkLabels
                    x1={lx1}
                    y1={ly1}
                    x2={lx2}
                    y2={ly2}
                    portA={l.a_port}
                    portB={peerPort}
                    speedText={speedText}
                    speedFill={!online ? st.stroke : "var(--text)"}
                    speedLetterSpacing={1}
                    bwA={!stpView && online && r.from ? `${towardA} ${fmtMbps(r.toA)}` : null}
                    bwB={!stpView && online && r.from ? `${towardB} ${fmtMbps(r.toB)}` : null}
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
              const mbps = Math.max(r.toA, r.toB);
              const st = topologyLinkStroke({
                online,
                blocking,
                util: drv.mbps > 0 ? mbps / drv.mbps : 0,
              });
              const towardA = x1 <= x2 ? "◂" : "▸";
              const towardB = x1 <= x2 ? "▸" : "◂";
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
                    ? st.stroke
                    : "var(--text)";
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
                  <line
                    x1={x1} y1={y1} x2={x2} y2={y2}
                    stroke={selected ? "var(--accent)" : st.stroke}
                    strokeWidth={selected ? st.strokeWidth + 1.5 : st.strokeWidth}
                    strokeDasharray={st.strokeDasharray}
                    filter={selected ? undefined : st.filter}
                    opacity={st.opacity}
                  />
                  <TopologyLinkLabels
                    x1={x1}
                    y1={y1}
                    x2={x2}
                    y2={y2}
                    portA={l.a_port}
                    portB={l.b_port}
                    speedText={midLabel + (l.note && online ? " ⚠" : "")}
                    speedFill={midFill}
                    speedLetterSpacing={1}
                    bwA={!stpView && online && r.from ? `${towardA} ${fmtMbps(r.toA)}` : null}
                    bwB={!stpView && online && r.from ? `${towardB} ${fmtMbps(r.toB)}` : null}
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
            const devPorts = expandPorts(d).filter(
              (pr) => pr.p.mode && pr.p.mode !== "libre" && pr.p.tagged !== "all",
            );
            const up = expandPorts(d).filter((pr) => pr.p.link && pr.p.mode !== "libre").length;
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
                  text={`${d.name} — ${d.model}${d.location ? " · " + d.location : ""}\n${t("planPage.configuredPorts", { configured: devPorts.length, active: up })}`}
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
                        {modelShort(d.model, d.vendor)}
                      </div>
                    </QuickTip>
                    <div style={{ display: "flex", flexWrap: "wrap", gap: 2, marginLeft: 15 }}>
                      {devPorts.map((pr) => (
                        <PortSquare key={pr.name} name={pr.name} port={pr.p} vm={vm} size={9} off={off} />
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


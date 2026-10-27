// The CLIENTS page: every MAC the fleet's bridges have learned, located at its
// edge port, identified, and metered.
//
// The backend assembles the row (location arbitration, OUI identity, LLDP
// fingerprint, port telemetry, bandwidth bucket — internal/api/clients_handlers.go);
// this page owns what a table view owns: SORT on any column, MULTI-FILTER on
// switch and VLAN, a MAC search, and a refresh cadence. Filtering is
// client-side because the whole site's list is one payload anyway — a filter
// that round-tripped to the server would redraw slower and filter the same rows.
//
// The one WRITE on this screen is the broom (`DELETE …/clients?days=N`): the
// ledger is append-only by design, so nothing but an explicit gesture can ever
// shorten the list. It is armed before it fires.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ageMs } from "../ui/clock";
import type { TFunction } from "i18next";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { del, get, type ClientRow, type SiteClients } from "../api";
import { useSiteRole } from "../auth";
import {
  ActionButton,
  DeleteButton,
  ErrorMsg,
  HelpTip,
  Modal,
  QuickTip,
  useToast,
} from "../components";
import { macMatches, parseMacQuery } from "./macSearch";
import { PillMultiSelect } from "../PillMultiSelect";
import { PortTrafficCell } from "../PortTraffic";
import { deviceDetailPath } from "../paths";
import { useBreakpoint } from "../ui/breakpoint";
import { useSite, useSiteId } from "../useSite";
import { decimalSeparator, fmtMbps } from "../network";
import { VendorBadge } from "../vendorBadge";

/** Refresh cadence: the collection itself moves once a minute (FDB cadence). */
const CLIENTS_REFETCH_MS = 30_000;

/**
 * The broom's windows, in the order a doubt resolves.
 *
 * Days and not an arbitrary field: the question is never "how many days" but
 * "is this thing still on my network", and the five answers to that are a
 * day, a few days, a week, a month, half a year. A free number would be a
 * form to fill in before a gesture that is already a decision.
 */
const PRUNE_WINDOWS: { days: number; key: string }[] = [
  { days: 1, key: "clientsPage.cleanDay1" },
  { days: 3, key: "clientsPage.cleanDay3" },
  { days: 7, key: "clientsPage.cleanWeek1" },
  { days: 30, key: "clientsPage.cleanMonth1" },
  { days: 180, key: "clientsPage.cleanMonth6" },
];

/**
 * The speed LABEL the traffic ramp reads against ("1G", "10G").
 *
 * PortTrafficCell colours its curve against a line rate it parses out of a
 * group's speed label, which is what the Ports page has; the Clients row
 * carries the negotiated speed as a NUMBER instead. Rendering it back into the
 * label grammar is the whole adaptation — and a port whose speed the series
 * never reported gets `undefined`, where `speedMbps` falls back to a gigabit
 * (the right guess on this fleet) rather than to a zero that would paint every
 * curve as saturated.
 */
function speedLabel(mbps: number | undefined): string | undefined {
  if (!mbps || mbps <= 0) return undefined;
  if (mbps >= 1000) return mbps / 1000 + "G";
  return mbps + "M";
}

type SortKey =
  | "vendor"
  | "mac"
  | "name"
  | "device"
  | "port"
  | "vlan"
  | "speed"
  | "volume"
  | "last_seen"
  | "first_seen";

/** Human bytes, decimal units — the vocabulary of link budgets, not of RAM. */
function fmtBytes(n: number, sep: string): string {
  if (!n) return "—";
  const units = ["B", "kB", "MB", "GB", "TB", "PB"];
  let v = n;
  let u = 0;
  while (v >= 1000 && u < units.length - 1) {
    v /= 1000;
    u++;
  }
  const txt = v >= 100 || u === 0 ? Math.round(v).toString() : v.toFixed(1).replace(".", sep);
  return txt + " " + units[u];
}

/** Relative time, coarse on purpose: "3 min" answers presence, not forensics. */
function ago(iso: string, t: TFunction): string {
  const ms = ageMs(iso) ?? 0;
  if (!Number.isFinite(ms) || ms < 0) return "—";
  const s = Math.round(ms / 1000);
  if (s < 90) return t("clientsPage.agoSeconds", { n: s });
  if (s < 5400) return t("clientsPage.agoMinutes", { n: Math.round(s / 60) });
  if (s < 48 * 3600) return t("clientsPage.agoHours", { n: Math.round(s / 3600) });
  return t("clientsPage.agoDays", { n: Math.round(s / 86400) });
}

function cmp(a: ClientRow, b: ClientRow, key: SortKey): number {
  switch (key) {
    case "vendor":
      return (a.vendor ?? "").localeCompare(b.vendor ?? "");
    case "mac":
      return a.mac.localeCompare(b.mac);
    case "name":
      return (a.name ?? "").localeCompare(b.name ?? "");
    // Switch, then PORT inside it. A list ordered by switch is read by
    // walking one closet at a time, and inside a closet the only order that
    // means anything is the physical one; without the tie-break the rows of
    // a single switch came out in whatever order the payload happened to
    // carry. It also makes "switch, then port" ONE entry in the narrow
    // sort menu instead of a two-step the menu cannot express.
    case "device": {
      const byDevice = a.device_name.localeCompare(b.device_name);
      if (byDevice !== 0) return byDevice;
      return a.port.localeCompare(b.port, undefined, { numeric: true });
    }
    case "port":
      return a.port.localeCompare(b.port, undefined, { numeric: true });
    case "vlan":
      return (a.vlans[0] ?? 0) - (b.vlans[0] ?? 0);
    case "speed":
      return (a.speed_mbps ?? 0) - (b.speed_mbps ?? 0);
    case "volume":
      return a.rx_bytes + a.tx_bytes - (b.rx_bytes + b.tx_bytes);
    case "last_seen":
      return a.last_seen.localeCompare(b.last_seen);
    case "first_seen":
      return a.first_seen.localeCompare(b.first_seen);
  }
}

/**
 * THE SORT, ONCE THE HEADERS ARE GONE.
 *
 * Ten sortable headers are ten controls that only exist because a table has
 * a top edge. Under bp:narrow there is no top edge, and dropping the sort
 * with the headers would quietly remove a third of what this page does —
 * so it moves into a control of its own, as a `<select>`: the platform
 * already renders one as a full-height picker on a phone, which is the one
 * list control a thumb never misses.
 *
 * Each entry is a whole ORDERING, direction included, named as the question
 * it answers ("longest unseen") rather than as a column plus an arrow
 * ("last seen, ascending" — which of the two ends is "ascending" on a date
 * is a coin toss every time). It drives the SAME `sortKey`/`sortAsc` the
 * headers drive, so rotating a phone to landscape lands on the table still
 * sorted the way the menu left it.
 *
 * All ten sortable columns are here — `traffic` is absent for the same
 * reason its header is flat. The two reversals that earn a second entry are
 * the dates, because "most recent" and "longest unseen" are both real
 * questions and the second one is how the broom is aimed.
 */
const SORT_ORDERINGS: { key: SortKey; asc: boolean; label: string }[] = [
  { key: "last_seen", asc: false, label: "clientsPage.sortSeenNew" },
  { key: "last_seen", asc: true, label: "clientsPage.sortSeenOld" },
  { key: "device", asc: true, label: "clientsPage.sortWhere" },
  { key: "port", asc: true, label: "clientsPage.sortPort" },
  { key: "vlan", asc: true, label: "clientsPage.sortVlan" },
  { key: "vendor", asc: true, label: "clientsPage.sortVendor" },
  { key: "name", asc: true, label: "clientsPage.sortName" },
  { key: "mac", asc: true, label: "clientsPage.sortMac" },
  { key: "volume", asc: false, label: "clientsPage.sortVolume" },
  { key: "speed", asc: false, label: "clientsPage.sortSpeed" },
  { key: "first_seen", asc: false, label: "clientsPage.sortFirstNew" },
  { key: "first_seen", asc: true, label: "clientsPage.sortFirstOld" },
];

const orderingValue = (key: SortKey, asc: boolean) => key + (asc ? ":a" : ":d");

/**
 * ONE CLIENT AS A CARD — the narrow form of a table row, the rule
 * UI-GUIDE §9.1 has promised since it was written ("tables → cards").
 *
 * The table it replaces was ten columns wide and kept all of them on a
 * phone behind a horizontal scroll, which showed "Vendor", "MAC address"
 * and half of a third. Two columns of ten is not a small table, it is a
 * table you cannot read.
 *
 * So the card is ordered by the QUESTION the page exists to answer — "where
 * is this machine plugged in" — and not by the table's left-to-right:
 *
 *   1. WHAT IT IS, and whether it is still there: the vendor badge and,
 *      opposite it, the recency with its presence dot. Those two are what
 *      lets a row be dismissed without reading the rest of it.
 *   2. WHICH ONE: the MAC, in mono, on its own line, never truncated — it
 *      is the identifier the operator arrived with in the clipboard, and a
 *      clipped identifier reads as a different address.
 *   3. WHERE: switch · port · VLAN, together on one line, because they are
 *      one fact and not three. This is THE ANSWER and it is the line the
 *      eye should land on.
 *   4. HOW MUCH: the sparkline, then speed, volume, first sighting.
 *
 * What was CUT rather than shrunk: nothing. Every column survives, three of
 * them demoted — the announced name only renders when the client announced
 * one (most do not, and an empty "Announced name — " line per card is the
 * table's emptiness with extra steps), the description and the volume split
 * stay one hover away, and `first_seen` joins the number line instead of
 * holding a column of its own.
 *
 * The one thing deliberately NOT done here is the device page's trick of
 * letting a block scroll sideways INSIDE the card (`card-ports--mobile` in
 * Equipements.tsx). That is right for a port facade, whose physical order
 * is the information and which therefore must not wrap; it is wrong for
 * these fields, which are independent facts that wrap perfectly well and
 * whose sideways scroll would be the very defect this card exists to end.
 */
function ClientCard({
  r,
  siteSlug,
  sep,
  t,
}: {
  r: ClientRow;
  siteSlug: string;
  sep: string;
  t: TFunction;
}) {
  return (
    <li className="clients-card">
      <div className="clients-card-head">
        <VendorBadge
          vendor={r.vendor}
          randomized={r.randomized}
          labelWhenRandomized={t("clientsPage.randomized")}
          labelWhenUnknown={t("clientsPage.unknownVendor")}
        />
        <QuickTip
          text={r.online ? t("clientsPage.online") : t("clientsPage.offline")}
          className="quicktip-inline"
        >
          <span className="clients-card-seen">
            <span className={"dot clients-card-dot" + (r.online ? " is-on" : "")} />
            {ago(r.last_seen, t)}
          </span>
        </QuickTip>
      </div>

      <div className="clients-card-mac mono">{r.mac}</div>

      {r.name ? (
        <QuickTip text={r.description || undefined} className="quicktip-inline">
          <div className="clients-card-name">{r.name}</div>
        </QuickTip>
      ) : null}

      <div className="clients-card-where">
        <Link to={deviceDetailPath(siteSlug, { id: r.device_id, name: r.device_name })}>
          {r.device_name}
        </Link>
        <span className="clients-card-port mono">{r.port}</span>
        {r.elsewhere ? (
          <HelpTip icon="i">{t("clientsPage.elsewhere", { n: r.elsewhere })}</HelpTip>
        ) : null}
        {r.vlans.length === 0 ? (
          <QuickTip text={t("clientsPage.vlanUnknown")} className="quicktip-inline">
            <span className="clients-card-vlan note">{t("clientsPage.vlanNone")}</span>
          </QuickTip>
        ) : (
          <span className="clients-card-vlan">
            {t("clientsPage.vlanTag", { v: r.vlans.join(", ") })}
          </span>
        )}
      </div>

      <div className="clients-card-foot">
        {/* The sparkline is kept at full size and NOT shrunk with the rest:
            it is one of the very few things on this screen that reads as
            well at 393px as at 1440px, because what it carries is a shape
            and a shape does not need a column header to be understood. */}
        {r.link_up ? (
          <PortTrafficCell deviceId={r.device_id} port={r.port} speed={speedLabel(r.speed_mbps)} />
        ) : (
          <span className="spark-empty">—</span>
        )}
        <span className="clients-card-nums note">
          <span>{r.link_up ? fmtMbps(r.speed_mbps ?? 0) : t("clientsPage.linkDown")}</span>
          <QuickTip
            text={t("clientsPage.volumeSplit", {
              rx: fmtBytes(r.rx_bytes, sep),
              tx: fmtBytes(r.tx_bytes, sep),
            })}
            className="quicktip-inline"
          >
            <span>{fmtBytes(r.rx_bytes + r.tx_bytes, sep)}</span>
          </QuickTip>
          <span>{t("clientsPage.sinceFirst", { when: ago(r.first_seen, t) })}</span>
        </span>
      </div>
    </li>
  );
}

export default function Clients() {
  const { t } = useTranslation();
  const { siteSlug } = useSite();
  const siteId = useSiteId();
  const sep = decimalSeparator();
  const role = useSiteRole(siteId);
  const qc = useQueryClient();
  const [toast, showToast] = useToast();
  // Below 720px the table becomes a list of cards (UI-GUIDE §9.1). Branching
  // in JS rather than hiding one of two rendered trees in CSS, because the
  // tree that is not shown must not exist: each visible row mounts a
  // PortTrafficCell, and a hidden duplicate table would double the work the
  // page does for a reader who can never see it.
  const narrow = useBreakpoint() === "narrow";

  const [sortKey, setSortKey] = useState<SortKey>("last_seen");
  const [sortAsc, setSortAsc] = useState(false);
  // MULTI-filters: empty = no filter. Arrays and not Sets because they feed
  // PillMultiSelect, whose contract is the ordered list the pills display —
  // the order the operator picked them in is the order they read back.
  // VLANs ride as STRINGS ("99") for the same reason: the control's ids are
  // strings, and parsing back at the one place that compares beats a parallel
  // numeric state that can drift from what the pills show.
  const [devFilter, setDevFilter] = useState<string[]>([]);
  const [vlanFilter, setVlanFilter] = useState<string[]>([]);
  // The MAC search keeps the RAW text, not the parsed term: what the operator
  // typed is what the field must keep showing, separators and all — a field
  // that rewrote a paste into our own spelling would fight every paste.
  const [macText, setMacText] = useState("");
  // The broom. `cleanOpen` is the modal; `cleanDays` survives it being closed
  // and reopened, because the window an operator chose once is almost always
  // the one they want on the second pass.
  //
  // It OPENS ON THE WIDEST window, which is the one that deletes least. A
  // default on a destructive dialog is a choice made for someone who has not
  // read it yet, so it goes to the end of the range where being wrong costs
  // the least — narrowing it is then a deliberate act, and the count below
  // the radios moves as it narrows.
  const [cleanOpen, setCleanOpen] = useState(false);
  const [cleanDays, setCleanDays] = useState(
    PRUNE_WINDOWS[PRUNE_WINDOWS.length - 1].days,
  );

  const { data, error, dataUpdatedAt } = useQuery({
    queryKey: ["clients", siteId],
    queryFn: () => get<SiteClients>(`/api/sites/${siteId}/clients`),
    enabled: !!siteId,
    refetchInterval: CLIENTS_REFETCH_MS,
  });

  const rows = useMemo(() => data?.clients ?? [], [data]);

  const prune = useMutation({
    mutationFn: (days: number) =>
      del<{ clients: number; sightings: number }>(
        `/api/sites/${siteId}/clients?days=${days}`,
      ),
    onSuccess: (res) => {
      setCleanOpen(false);
      // Both halves of the page's source are gone server-side; the list is
      // re-read rather than patched, because the row count is a JOIN and the
      // server is the only thing that knows what survived it.
      void qc.invalidateQueries({ queryKey: ["clients", siteId] });
      showToast(t("clientsPage.cleanDone", { n: res.clients }));
    },
  });

  // Filter option lists come from the DATA, not the inventory: a switch with
  // no located client is noise in a filter menu. Shaped for PillMultiSelect —
  // the number of clients rides as the hint, so the dropdown answers "is it
  // worth filtering on this one" before the click.
  const deviceOptions = useMemo(() => {
    const names = new Map<string, string>();
    const counts = new Map<string, number>();
    rows.forEach((r) => {
      names.set(r.device_id, r.device_name);
      counts.set(r.device_id, (counts.get(r.device_id) ?? 0) + 1);
    });
    return [...names.entries()]
      .sort((a, b) => a[1].localeCompare(b[1]))
      .map(([id, label]) => ({
        id,
        label,
        hint: t("clientsPage.filterCount", { n: counts.get(id) ?? 0 }),
      }));
  }, [rows, t]);
  const vlanOptions = useMemo(() => {
    const counts = new Map<number, number>();
    rows.forEach((r) => r.vlans.forEach((v) => counts.set(v, (counts.get(v) ?? 0) + 1)));
    return [...counts.keys()]
      .sort((a, b) => a - b)
      .map((v) => ({
        id: String(v),
        label: String(v),
        hint: t("clientsPage.filterCount", { n: counts.get(v) ?? 0 }),
      }));
  }, [rows, t]);

  const macQuery = useMemo(() => parseMacQuery(macText), [macText]);

  const shown = useMemo(() => {
    let out = rows;
    if (devFilter.length > 0) {
      const want = new Set(devFilter);
      out = out.filter((r) => want.has(r.device_id));
    }
    if (vlanFilter.length > 0) {
      const want = new Set(vlanFilter.map(Number));
      out = out.filter((r) => r.vlans.some((v) => want.has(v)));
    }
    // The MAC search last: it is the narrowest of the three and the one most
    // often typed into an already-filtered list.
    if (macQuery.kind !== "off") out = out.filter((r) => macMatches(r.mac, macQuery));
    const dir = sortAsc ? 1 : -1;
    return [...out].sort((a, b) => dir * cmp(a, b, sortKey));
  }, [rows, devFilter, vlanFilter, macQuery, sortKey, sortAsc]);

  // What the chosen window would take off THIS list, so the gesture is read
  // as a number before it is armed. It is a floor, not the total: the ledger
  // also holds MACs with no current location (the "known" minus "located" of
  // the summary), which the server will forget too and the page cannot count.
  const cleanHits = useMemo(() => {
    // ADR-0008: `ageMs` subtracts inside ONE frame. The previous form built a
    // cutoff from Date.now() — the viewer's clock — and compared it against a
    // raw server timestamp, which is the mixed-frame subtraction that ADR
    // exists to delete. It mattered more here than on a label: this number is
    // what arms a DELETION, so a skewed clock made the page promise a count
    // the server would not honour.
    const window = cleanDays * 86400_000;
    return rows.filter((r) => (ageMs(r.last_seen) ?? 0) > window).length;
  }, [rows, cleanDays]);

  const filtering = devFilter.length > 0 || vlanFilter.length > 0 || macQuery.kind !== "off";

  const clickSort = (key: SortKey) => {
    if (key === sortKey) setSortAsc(!sortAsc);
    else {
      setSortKey(key);
      // Text columns open ascending, quantities descending — each column
      // opens on the reading its header promises.
      setSortAsc(["vendor", "mac", "name", "device", "port", "vlan"].includes(key));
    }
  };
  const arrow = (key: SortKey) => (key !== sortKey ? "" : sortAsc ? " ▲" : " ▼");

  // The same three answers to "why is this list empty", shared by the table
  // and by the card list — an emptiness that reads differently depending on
  // the width would be a bug, not a layout.
  const emptyNote =
    rows.length === 0
      ? t("clientsPage.emptyCollecting")
      : macQuery.kind === "impossible"
        ? t("clientsPage.emptyMacNotAMac")
        : t("clientsPage.emptyFiltered");

  const th = (key: SortKey, label: string, width?: number) => (
    <th
      style={{ cursor: "pointer", ...(width ? { width } : {}) }}
      onClick={() => clickSort(key)}
      role="columnheader"
      aria-sort={key === sortKey ? (sortAsc ? "ascending" : "descending") : "none"}
    >
      {label}
      {arrow(key)}
    </th>
  );

  return (
    <div className="page">
      <main>
        <div className="card">
          {/* No heading in the head: the tab bar names this page. The counts
              stay exactly where they were — this head is where they are read
              (which is why the broom lives with the filters and not here) —
              they simply no longer sit beside a title repeating the tab. */}
          <div className="card-head">
            <span className="note">
              {t("clientsPage.summary", {
                shown: shown.length,
                located: data?.counts.clients ?? 0,
                known: data?.counts.known ?? 0,
              })}
              {dataUpdatedAt
                ? " · " + t("clientsPage.updated", { when: ago(new Date(dataUpdatedAt).toISOString(), t) })
                : null}
            </span>
          </div>

          {/* The two MULTI-filters, as typing fields: each is a PillMultiSelect
              — type to narrow the dropdown, pick to grow a removable pill.
              This replaced one chip per switch and per VLAN laid flat: that
              row was the whole option list permanently on screen, which reads
              at five switches and stops reading at forty (the same argument
              that created PillMultiSelect for the root-bridge picker — the
              SELECTION is what deserves the space, the catalogue only while
              choosing). */}
          <div className="clients-filters">
            <div className="clients-filter clients-filter--half">
              <span className="kicker">{t("clientsPage.filterSwitch")}</span>
              <PillMultiSelect
                options={deviceOptions}
                selected={devFilter}
                onChange={setDevFilter}
                placeholder={t("clientsPage.filterAllPh")}
                ariaLabel={t("clientsPage.filterSwitch")}
              />
            </div>
            <div className="clients-filter clients-filter--half">
              <span className="kicker">VLAN</span>
              <PillMultiSelect
                options={vlanOptions}
                selected={vlanFilter}
                onChange={setVlanFilter}
                placeholder={t("clientsPage.filterAllPh")}
                ariaLabel="VLAN"
              />
            </div>
            {/* THE MAC SEARCH. A plain field and not a PillMultiSelect: one
                looks a single address up, and the catalogue of every MAC on
                the site is not a menu anyone picks from. `type="search"`
                because the browser's own clear affordance is the one a phone
                keyboard already offers.

                What makes it usable is in macSearch.ts, not here: the five
                written forms of a MAC (attached, colons, dashes, dotted
                pairs, Cisco quads) all reduce to one key, so the thing in the
                clipboard finds the row whatever console it came from, and a
                middle fragment matches. */}
            <div className="clients-filter">
              <span className="kicker">{t("clientsPage.macCol")}</span>
              <input
                className="clients-mac-search"
                type="search"
                autoComplete="off"
                spellCheck={false}
                value={macText}
                onChange={(e) => setMacText(e.target.value)}
                placeholder={t("clientsPage.macSearchPh")}
                aria-label={t("clientsPage.macSearchLabel")}
              />
            </div>
            {/* THE SORT, where the headers used to be — narrow only, since
                wide still has ten of them and a second control saying the
                same thing twice is a second place to get it wrong. */}
            {narrow && (
              <div className="clients-filter">
                <span className="kicker">{t("clientsPage.sortLabel")}</span>
                <select
                  className="clients-sort"
                  aria-label={t("clientsPage.sortLabel") as string}
                  value={orderingValue(sortKey, sortAsc)}
                  onChange={(e) => {
                    const found = SORT_ORDERINGS.find(
                      (o) => orderingValue(o.key, o.asc) === e.target.value,
                    );
                    if (!found) return;
                    setSortKey(found.key);
                    setSortAsc(found.asc);
                  }}
                >
                  {/* An ordering the headers can reach but the menu does not
                      name (a wide reader clicked "Volume" into ascending,
                      then narrowed the window). It is shown rather than
                      silently snapped to a neighbour, because the list in
                      front of the reader really is in that order. */}
                  {!SORT_ORDERINGS.some((o) => o.key === sortKey && o.asc === sortAsc) && (
                    <option value={orderingValue(sortKey, sortAsc)}>
                      {t("clientsPage.sortOther")}
                    </option>
                  )}
                  {SORT_ORDERINGS.map((o) => (
                    <option key={orderingValue(o.key, o.asc)} value={orderingValue(o.key, o.asc)}>
                      {t(o.label)}
                    </option>
                  ))}
                </select>
              </div>
            )}
            {filtering && (
              <ActionButton
                kind="close"
                className="ghost clients-clear"
                onClick={() => {
                  setDevFilter([]);
                  setVlanFilter([]);
                  setMacText("");
                }}
              >
                {t("clientsPage.clearFilters")}
              </ActionButton>
            )}
            {/* The broom sits with the controls and not in the card head: it
                is the one write on a read-only screen, and the head is where
                the counts are read. Hidden outright for a viewer — the server
                refuses it at `operator`, and a button that only ever answers
                403 is worse than no button. */}
            {role !== "viewer" && (
              <span className="clients-broom">
                <ActionButton kind="delete" className="ghost" onClick={() => setCleanOpen(true)}>
                  {t("clientsPage.cleanAction")}
                </ActionButton>
              </span>
            )}
          </div>

          <ErrorMsg error={error} />
          {narrow ? (
            /* A real <ul>: this is a list of things, it was a list of things
               when it was a <table>, and a screen reader on a phone should
               still be told how many. */
            <ul className="clients-cards">
              {shown.map((r) => (
                <ClientCard key={r.mac} r={r} siteSlug={siteSlug ?? ""} sep={sep} t={t} />
              ))}
              {shown.length === 0 && !error && <li className="note">{emptyNote}</li>}
            </ul>
          ) : (
          <div className="scroll-x scroll-cue">
            <table className="ports-table clients-table">
              <thead>
                <tr>
                  {th("vendor", t("clientsPage.vendorCol"), 190)}
                  {th("mac", t("clientsPage.macCol"), 150)}
                  {th("name", t("clientsPage.nameCol"))}
                  {th("device", t("clientsPage.switchCol"), 140)}
                  {th("port", t("clientsPage.portCol"), 90)}
                  {th("vlan", "VLAN", 90)}
                  {th("speed", t("clientsPage.speedCol"), 90)}
                  {/* NOT sortable, and the only column that is not. The curve
                      is fetched per DEVICE by the cell itself, so the page
                      does not hold the numbers it would have to sort on —
                      a header that sorted on nothing is worse than one that
                      plainly does not. */}
                  <th className="clients-th-flat">{t("clientsPage.trafficCol")}</th>
                  {th("volume", t("clientsPage.volumeCol"), 110)}
                  {th("last_seen", t("clientsPage.seenCol"), 110)}
                  {th("first_seen", t("clientsPage.firstCol"), 110)}
                </tr>
              </thead>
              <tbody>
                {shown.map((r) => (
                  <tr key={r.mac}>
                    <td>
                      <VendorBadge
                        vendor={r.vendor}
                        randomized={r.randomized}
                        labelWhenRandomized={t("clientsPage.randomized")}
                        labelWhenUnknown={t("clientsPage.unknownVendor")}
                      />
                    </td>
                    <td className="mono">{r.mac}</td>
                    <td title={r.description || undefined}>
                      {r.name || <span className="note">—</span>}
                    </td>
                    <td>
                      <Link to={deviceDetailPath(siteSlug ?? "", { id: r.device_id, name: r.device_name })}>
                        {r.device_name}
                      </Link>
                    </td>
                    <td>
                      {r.port}
                      {r.elsewhere ? (
                        <HelpTip icon="i">{t("clientsPage.elsewhere", { n: r.elsewhere })}</HelpTip>
                      ) : null}
                    </td>
                    <td>
                      {r.vlans.length === 0 ? (
                        <span className="note" title={t("clientsPage.vlanUnknown")}>
                          —
                        </span>
                      ) : (
                        r.vlans.join(", ")
                      )}
                    </td>
                    <td>{r.link_up ? fmtMbps(r.speed_mbps ?? 0) : <span className="note">—</span>}</td>
                    {/* The SHAPE of the client's traffic, not another number.
                        Volume beside it is a lifetime total and speed is a
                        capacity; neither says whether this machine is doing
                        anything right now, which is the question asked of a
                        client list.

                        It costs one request PER SWITCH, not per row:
                        PortTrafficCell queries on the device and react-query
                        collapses the cells of one switch onto a single cache
                        entry (see PortTraffic.tsx). `off` when the link is
                        down — a curve from before the unplug would read as
                        live traffic on a port that has none. */}
                    <td className="ports-traffic-cell">
                      {r.link_up ? (
                        <PortTrafficCell
                          deviceId={r.device_id}
                          port={r.port}
                          speed={speedLabel(r.speed_mbps)}
                        />
                      ) : (
                        <span className="spark-empty">—</span>
                      )}
                    </td>
                    <td title={t("clientsPage.volumeSplit", {
                      rx: fmtBytes(r.rx_bytes, sep),
                      tx: fmtBytes(r.tx_bytes, sep),
                    })}>
                      {fmtBytes(r.rx_bytes + r.tx_bytes, sep)}
                    </td>
                    <td>
                      <span
                        className="dot"
                        style={{
                          background: r.online ? "var(--ok)" : "var(--border)",
                          marginRight: 6,
                        }}
                        title={r.online ? t("clientsPage.online") : t("clientsPage.offline")}
                      />
                      {ago(r.last_seen, t)}
                    </td>
                    <td>{ago(r.first_seen, t)}</td>
                  </tr>
                ))}
                {shown.length === 0 && !error && (
                  <tr>
                    <td colSpan={11} className="note">
                      {emptyNote}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
          )}
        </div>

        {/* THE BROOM, as a modal. Three things it has to get right, in order:
            say what disappears, let the window be chosen before the gesture
            is armed, and refuse to fire on one click.

            `Modal` carries Escape, the focus trap and the busy refusal (so a
            delete in flight cannot be dismissed half-done), and under
            bp:narrow it is already a bottom sheet. The confirmation is
            DeleteButton's inline two-step rather than a second dialog: a
            modal on top of a modal buries the window that was just chosen,
            and the two-step keeps the choice on screen while it is confirmed. */}
        {cleanOpen && (
          <Modal
            title={t("clientsPage.cleanTitle")}
            onClose={() => setCleanOpen(false)}
            help={t("clientsPage.cleanHelp")}
            busy={prune.isPending}
          >
            <p className="note">{t("clientsPage.cleanIntro")}</p>
            <div className="clients-clean-windows" role="radiogroup" aria-label={t("clientsPage.cleanTitle")}>
              {PRUNE_WINDOWS.map((w) => (
                <label key={w.days} className="clients-clean-window">
                  <input
                    type="radio"
                    name="clients-clean-window"
                    checked={cleanDays === w.days}
                    disabled={prune.isPending}
                    onChange={() => setCleanDays(w.days)}
                  />
                  <span>{t(w.key)}</span>
                </label>
              ))}
            </div>
            <p className="note">
              {t("clientsPage.cleanEffect", { n: cleanHits, total: rows.length })}
            </p>
            <ErrorMsg error={prune.error} />
            <div className="actions">
              <ActionButton
                kind="cancel"
                className="ghost"
                onClick={() => setCleanOpen(false)}
                disabled={prune.isPending}
              >
                {t("common.cancel")}
              </ActionButton>
              {/* `key` is the window: changing the choice REMOUNTS the button
                  and drops its armed state. Without it, arming on "1 day" and
                  then picking "6 months" leaves a live confirm pointing at a
                  window nobody armed. */}
              <DeleteButton
                key={cleanDays}
                label={t("clientsPage.cleanConfirm")}
                onConfirm={() => prune.mutate(cleanDays)}
              />
            </div>
          </Modal>
        )}
        {toast}
      </main>
    </div>
  );
}

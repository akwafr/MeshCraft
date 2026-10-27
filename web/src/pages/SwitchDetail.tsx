import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { viewerDate } from "../ui/clock";
import { Suspense, lazy, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useGlobalKey } from "../ui/dismiss";
import { useNavigate, useParams } from "react-router-dom";
import { deviceDetailPath, deviceDetailPathAfterRename, findDeviceBySegment, sitePath } from "../paths";
import { useSite, useSiteId } from "../useSite";
import {
  useSiteLive,
  get,
  patch,
  post,
  put,
  type AddressDomain,
  type Device,
  type PortIntent,
  type PortProfileSpec,
  type Profile,
  type ProxyInfo,
  type Vlan,
} from "../api";
import { useSiteRole } from "../auth";
import { MgmtAddressingCard } from "./MgmtAddressing";
import { SpanningTreeCard, stpEffectiveMeta } from "./SpanningTreeDevice";
// LAZY on purpose: DeviceConsole pulls in xterm.js, a terminal emulator worth a
// quarter of a megabyte that the overwhelming majority of visits to this page
// never open. Loading it on demand keeps the switch sheet's first paint free of
// it, and the chunk arrives while the controller is still being asked what the
// device offers.
const DeviceConsole = lazy(() => import("./DeviceConsole"));
import {
  ActionButton,
  ErrorMsg,
  HelpTip,
  PillPicker,
  QuickTip,
  useToast,
} from "../components";
import {
  deviceRates,
  expandPorts,
  reachability,
  sinceLabel,
  serves,
  syncLabel,
  vlanMap,
  type PortRef,
} from "../network";
import { ProtocolPills, VlanLegend } from "../netui";
import { portConfigLines, profileNameMap } from "../portConfig";
import { portClasses, portLevels, portTraffic, TelemetryPanel, useTelemetry } from "../telemetry";
import { PortHoverTip, type PortHover } from "../PortHoverTip";
import { claimHoverTip, releaseHoverTip } from "../hoverExclusive";
// THE SECTIONS THIS PAGE COMPOSES. Each owns its own markup and nothing else;
// the page keeps the state, the queries, the mutations and the layout.
import { AddressDomainRow } from "./switchdetail/AddressDomainRow";
import { CollectorRow } from "./switchdetail/CollectorRow";
import { ConfigFreshness } from "./switchdetail/ConfigFreshness";
import { DeviceBanners } from "./switchdetail/DeviceBanners";
import { DeviceHeader } from "./switchdetail/DeviceHeader";
import { DeviceTags } from "./switchdetail/DeviceTags";
import { Faceplate } from "./switchdetail/Faceplate";
import { LagBuilder } from "./switchdetail/LagBuilder";
import { LagSidePanel } from "./switchdetail/LagSidePanel";
import { lagByMember, lagMemberSet, lagsOf, stripLagView, type Lag } from "../lags";
import { HostnameRow } from "./switchdetail/HostnameRow";
import { MultiPortPanel } from "./switchdetail/MultiPortPanel";
import { PortPanel } from "./switchdetail/PortPanel";
import { SidePanelGroup } from "./switchdetail/SidePanelGroup";
// POWER_CYCLE_OFF_SECONDS lives with the overlay that displays it: the number on
// screen and the number in the request must be the same one.
import { PortActionOverlay, POWER_CYCLE_OFF_SECONDS } from "./switchdetail/PortActionOverlay";
// The faceplate's two shape decisions, out of React so they can be asserted
// (facade.test.ts): which ports share a physical block, and which ones blink.
import { buildFacade, driftMap } from "./switchdetail/facade";


export default function SwitchDetail() {
  const { t, i18n } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  // The URL segment is ID-keyed with a name slug ("{name}--{uuid}"); old
  // name-only bookmarks still resolve (see findDeviceBySegment).
  const { deviceSeg } = useParams();
  const deviceSegment = deviceSeg ?? "";
  const navigate = useNavigate();
  const role = useSiteRole(siteId);
  const qc = useQueryClient();
  // The telemetry panel window, in minutes. On the form and not in the URL:
  // it's a reading setting, not a place to return to.
  const [teleMinutes, setTeleMinutes] = useState(1440);
  const [hoverVlan, setHoverVlan] = useState<number | null>(null);
  // {t("switchDetailPage.selection")} — multiple: click = one port, ⇧ = range
  // from the anchor, Ctrl/⌘ = adding or removing a port, ⇧+Ctrl/⌘ = adding a
  // range to the selection. The anchor is the last port clicked without ⇧:
  // that's where the range is measured from, same as everywhere else.
  const [selPorts, setSelPorts] = useState<string[]>([]);
  const [anchor, setAnchor] = useState<string | null>(null);
  const [selProf, setSelProf] = useState<string | null>(null);
  const [toast, showToast] = useToast();
  // The console overlay. Mounted only while open — xterm.js owns a canvas and a
  // WebSocket, and keeping it alive behind a hidden panel would hold a vty on a
  // switch that has sixteen of them.
  const [consoleOpen, setConsoleOpen] = useState(false);
  // The floating port chart on the faceplate — bandwidth, plus the optical
  // levels on a cage that has them. Same component and same query as the
  // equipment page and the topology, so the three cannot disagree.
  const [portHover, setPortHover] = useState<PortHover | null>(null);
  const facePortTipId = "switch-faceplate-port-tip";
  /**
   * The port whose ACTION overlay is up — shut/no-shut and PoE power cycle,
   * the two gestures that reach the hardware immediately and are not stored
   * anywhere (cf. internal/api/portadmin_apply.go: neither is an intent,
   * neither is reconciled).
   *
   * A port NAME and not a PortRef: `expandPorts` rebuilds its objects on every
   * render, so an overlay holding the object would keep repainting from a
   * snapshot taken before the last poll — and this is the one panel where the
   * live link state decides whether the confirm button is even enabled.
   */
  const [actionPort, setActionPort] = useState<string | null>(null);
  /**
   * THE SELECTED AGGREGATE (ADR-0007). A NAME, for the same reason actionPort
   * is one: the aggregate list is rebuilt from `observed ∪ intent` on every
   * poll, and a panel holding the object would describe a convergence state
   * that has since changed.
   *
   * Mutually exclusive with the port selection by construction — selecting an
   * aggregate clears the ports and the range anchor, selecting a port clears
   * this. An aggregate is an interface, so it owns the side panel exactly the
   * way a port does, and two things owning it at once is how a panel ends up
   * describing one thing and writing to another.
   */
  const [selLag, setSelLag] = useState<string | null>(null);
  /** null = closed; "" = creating; a name = editing that aggregate. */
  const [lagEditing, setLagEditing] = useState<string | null>(null);
  // Real-time: this is the sheet where we track a configuration being
  // applied. The state changes timing WITHOUT any user request —
  // automatic provisioning finishes several seconds after the last click
  // — and it's the controller that comes to announce it (see
  // emitDeviceBG in autoapply.go).
  const refetchInterval = useSiteLive(siteId);

  /**
   * ESCAPE CLEARS THE SELECTION — and gives way to everything that owns the key
   * before us.
   *
   * The gesture exists because the selection is sticky by design (it survives
   * polling, panel edits and profile applies), so "I am done with these twelve
   * ports" needed an answer that is not "find a piece of background to click".
   *
   * The four guards below are the whole difficulty, and each one closes a
   * concrete way of making the key feel broken:
   *
   *   - the ACTION OVERLAY and the CONSOLE are modal. Escape there means "close
   *     this", and clearing the selection underneath would silently change what
   *     the panel shows once the overlay is gone — including, in the overlay's
   *     case, the port the operator was about to act on;
   *   - a focused TEXT INPUT (port label, device name, address) uses Escape to
   *     revert what is being typed; a SELECT uses it to abandon an open
   *     dropdown. Neither is a request to drop a selection, and stealing the key
   *     there would cost the operator the field AND the selection in one press;
   *   - an OPEN HELP BUBBLE closes on Escape too (see HelpTip). It is detected
   *     through the `.on` class its own button carries rather than by lifting
   *     that state into a context: reading the DOM here keeps one gesture in one
   *     place, and a bubble the operator never opened cannot swallow the key.
   *
   * All three cases are now the SHARED guards of useGlobalKey (ui/dismiss.ts):
   * the overlays sit on the dismissal stack, so "one is open" is the stack's
   * answer rather than a prop list to keep in sync; the editable-focus and
   * help-bubble checks moved with it. The rationale above stays because it is
   * the WHY of those guards — this screen is where they were learned.
   */
  useGlobalKey("Escape", () => {
    setSelPorts([]);
    setSelProf(null);
    // The aggregate selection is a selection too, and Escape means the same
    // thing about it. The builder is a Modal, so it sits ABOVE us on the
    // dismissal stack and takes the key first — this never closes it.
    setSelLag(null);
  });

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
  const { data: profiles } = useQuery({
    queryKey: ["profiles", "port", siteId],
    queryFn: () => get<Profile[]>(`/api/sites/${siteId}/profiles/port`),
    enabled: !!siteId,
  });
  const dev = findDeviceBySegment(devices ?? [], deviceSegment);
  const deviceId = dev?.id ?? "";

  // TELEMETRY: the quantities no inventory field can carry, plus the
  // throughput history per port. Refreshed at the same rate as the device
  // sheet — it's the same screen, and two different cadences would give a
  // summary and a graph that contradict each other over time.
  const { data: tele, isFetching: teleFetching } = useTelemetry(
    deviceId,
    teleMinutes,
    refetchInterval,
  );

  // The ports that BLINK, and nothing else — flashing the whole faceplate for
  // the wrong port would send you searching for a long time to find what is
  // actually wrong. Pending/drift/applying come from the device (facade.ts).
  //
  // NOTE: a port action in flight also blinks, and that entry is added further
  // down — right after the mutations that know about it are declared. See the
  // comment there for why it cannot come through the pipeline above.
  const driftPorts = driftMap(dev);

  // The profile VOCABULARY the faceplate tips need: id → name, built once for
  // the whole facade rather than resolved per square. A port square carries a
  // `profile_id`, which is a UUID; without this map a hover could only say
  // "follows a profile", which is not an answer.
  //
  // `undefined` WHILE THE QUERY IS STILL IN FLIGHT, and that is not pedantry:
  // an empty map is a complete vocabulary that happens to contain nothing, so
  // a port carrying a real `profile_id` would be reported against it as a
  // DELETED group for the first few hundred milliseconds of every page load.
  // Saying nothing until the list arrives is the honest state.
  const profileNames = profiles ? profileNameMap(profiles) : undefined;

  // LINK AGGREGATION: observed ∪ intent (lags.ts), for the overlines and the
  // panel. Computed here once — the faceplate and the panel must agree on
  // which ports belong to which aggregate, or the colours would lie.
  const deviceLagsAll = dev ? lagsOf(dev) : { list: [], observedOnly: new Set<string>() };
  const deviceLags = deviceLagsAll.list;
  const lagMembers = lagByMember(deviceLags);
  // The aggregate the panel describes — resolved from the NAME every render,
  // so a poll that changed its convergence or its members is reflected.
  const selLagObj = deviceLags.find((l) => l.name === selLag) ?? null;
  // The members to emphasise on the faceplate (ADR-0007, decision 1).
  const lagEmphasis = lagMemberSet(deviceLags, selLagObj ? selLag : null);

  /**
   * Proofread the equipment. READ ONLY: replayable as much as you want, including
   * in production. This is the only way to notice a drift — without
   * rereading, meshcraft only knows its own intentions.
   */
  const readState = useMutation({
    mutationFn: () => post<{ ok: boolean; sync?: string; error?: string; snmp_reprovision?: string }>(
      `/api/devices/${deviceId}/read-state`, {}),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      if (!res.ok) return showToast(res.error ?? t("switchDetailPage.readFailedToast"));
      showToast(
        res.sync === "in_sync"
          ? t("switchDetailPage.deviceInSyncToast")
          : res.sync === "drift"
            ? t("switchDetailPage.deviceDriftToast")
            : t("switchDetailPage.configPendingToast"),
      );
      // A refresh on a device whose supervision is down also re-pushes SNMP in
      // the background: tell the operator it is under way, and refresh the
      // device shortly so snmp_status flips to ok without a second click.
      if (res.snmp_reprovision === "triggered") {
        showToast(t("switchDetailPage.snmpReprovisionToast"));
        setTimeout(() => qc.invalidateQueries({ queryKey: ["devices", siteId] }), 12000);
      }
    },
  });

  /**
   * THE ONE AGGREGATE WRITE. Save-and-apply, and it always carries the
   * COMPLETE desired list.
   *
   * Complete because the API's semantics are replace (an aggregate missing
   * from the list is dissolved — driver.LAGWriter), which makes a partial
   * send a destruction. Every caller below therefore builds the whole list:
   * the builder merges its draft over the rest, the dissolve filters one out.
   *
   * `stripLagView` is what keeps the body honest — `running` and `raw` are
   * observations, and the fallback VLAN travels only when it is actually set,
   * because a connector that does not declare the gesture refuses the whole
   * list on sight of the field rather than trimming it (ADR-0007).
   */
  const applyLags = useMutation({
    mutationFn: (lags: Lag[]) =>
      post<{ ok: boolean; result?: unknown; error?: string }>(
        `/api/devices/${deviceId}/lags`,
        { lags: lags.map(stripLagView) },
      ),
    onSuccess: () => {
      setLagEditing(null);
      // Re-read the device rather than trusting the draft: the overlines, the
      // aggregate cells and the panel then redraw from what the device
      // actually carries.
      readState.mutate();
      showToast(t("lag.applied"));
    },
  });

  /**
   * Reinstall the meshcraft configuration on the equipment.
   *
   * The connector REFUSES to rewrite the management VLAN — that of the plan as
   * the one that the equipment declares. An apply can therefore succeed by leaving
   * discrepancies: they are made as they are rather than passed over in silence.
   */
  const applyCfg = useMutation({
    mutationFn: (force?: boolean) => post<{
      ok: boolean;
      error?: string;
      drift?: { port: string }[];
      result?: { skipped?: string[]; not_applied?: { port?: string; kind: "guardrail" | "platform" | "device"; text: string }[] };
    }>(`/api/devices/${deviceId}/config/apply`, { force: !!force }),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      if (!res.ok) return showToast(res.error ?? t("switchDetailPage.configNotAppliedToast"));
      const restant = res.drift?.length ?? 0;
      const ecarte =
        (res.result?.skipped?.length ?? 0) + (res.result?.not_applied?.length ?? 0);
      showToast(
        restant === 0
          ? t("switchDetailPage.configAppliedVerifiedToast")
          : t("switchDetailPage.configAppliedDriftToast", { remaining: restant, skipped: ecarte ? t("switchDetailPage.configAppliedSkippedSuffix", { count: ecarte }) : "" }),
      );
    },
  });

  const importCfg = useMutation({
    mutationFn: () => post(`/api/devices/${deviceId}/config/import`, {}),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      qc.invalidateQueries({ queryKey: ["vlans", siteId] });
      showToast(t("switchDetailPage.configImported"));
    },
  });
  const vm = vlanMap(vlans);

  // One port goes through the BATCH endpoint on purpose: it carries the port
  // name in the request BODY, not the URL path. Port names on some platforms
  // contain a slash (FS "ethernet 1/11", Cisco "Gi1/0/1"); in the path
  // `/ports/{port}` that slash splits the route and the single-port edit hit
  // nothing — which is why a profile applied to one FS port did nothing while a
  // whole-switch apply (already body-based) worked.
  const setPort = useMutation({
    mutationFn: (args: { port: string; intent: PortIntent }) =>
      put<Device>(`/api/devices/${deviceId}/ports`, { ports: { [args.port]: args.intent } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["devices", siteId] }),
  });
  // Batch: a selection is a single gesture, it is sent in a single request —
  // applied in one block on the base side, and logged in one line.
  const setPorts = useMutation({
    mutationFn: (ports: Record<string, PortIntent>) =>
      put<Device>(`/api/devices/${deviceId}/ports`, { ports }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["devices", siteId] }),
  });
  // Edition of equipment metadata: name, location (grouping of
  // switches on the plan), management address.
  //
  // # Why an OPTIMISTIC display, and not a plain `invalidateQueries`
  //
  // This PATCH is not instantaneous: the controller takes the opportunity
  // to replay the auto-tags AND reprovision SNMP, which will TALK to the
  // equipment through the proxy. Measured on a real switch, the response
  // takes ~5s — versus 5ms for a simple list replay. Refreshing only once
  // the response comes back therefore leaves the screen frozen for five
  // seconds after a gesture the operator just made: they conclude that
  // nothing happened, reload the page, and discover that it actually did.
  //
  // This is not a way to hide latency: it's the only way to stay HONEST on
  // screen. We display the intention immediately, the server's truth takes
  // over once the response lands (`onSettled` rereads), and a failure
  // ROLLS BACK instead of leaving a made-up value.
  //
  // Don't forget `cancelQueries`: a refetch triggered BEFORE the PATCH
  // could land after it and overwrite the optimistic value with the old one.
  const saveDevice = useMutation({
    mutationFn: (fields: Partial<Device>) => {
      const d = (devices ?? []).find((x) => x.id === deviceId)!;
      return patch<Device>(`/api/devices/${deviceId}`, {
        group_id: d.group_id, name: d.name, model: d.model, vendor: d.vendor,
        location: d.location, mgmt_ip: d.mgmt_ip, status: d.status,
        status_note: d.status_note, poe_budget_w: d.poe_budget_w,
        pos_x: d.pos_x, pos_y: d.pos_y,
        ...fields,
      });
    },
    onMutate: async (fields: Partial<Device>) => {
      await qc.cancelQueries({ queryKey: ["devices", siteId] });
      const previous = qc.getQueryData<Device[]>(["devices", siteId]);
      // `...fields` as-is: a REMOVED location is worth an empty string, and
      // it must pass through this stage like any other value — a
      // `fields.location || d.location` would silently lose it here.
      qc.setQueryData<Device[]>(["devices", siteId], (list) =>
        (list ?? []).map((d) => (d.id === deviceId ? { ...d, ...fields } : d)),
      );
      // The ID half of the URL survives any rename, but the SLUG half must
      // move with the optimistic rename to stay readable — and a name-only
      // bookmark gets upgraded to the ID-keyed form on the way. `replace`,
      // not `push`: the old spelling is not a place to come back to.
      const renamed = deviceDetailPathAfterRename(siteSlug, deviceSegment, deviceId, fields.name);
      if (renamed) navigate(renamed, { replace: true });
      return { previous, renamedFrom: renamed ? dev?.name : undefined };
    },
    onError: (err, _fields, ctx) => {
      // Without this rollback, the screen would indefinitely keep a value
      // that the controller refused — the worst of both worlds.
      if (ctx?.previous) qc.setQueryData(["devices", siteId], ctx.previous);
      // The URL rolls back with the name: the cache is back to the old
      // name, so the slug would lie about the value on screen.
      if (ctx?.renamedFrom)
        navigate(deviceDetailPath(siteSlug, { id: deviceId, name: ctx.renamedFrom }), { replace: true });
      showToast(err instanceof Error ? err.message : t("switchDetailPage.updateFailedToast"));
    },
    onSuccess: (out, fields) => {
      // The server has the last word on the name too: if it normalized what
      // was sent, follow ITS spelling — the list will carry out.name, and
      // the slug must keep matching it.
      if (fields.name !== undefined && out.name !== fields.name)
        navigate(deviceDetailPath(siteSlug, { id: deviceId, name: out.name }), { replace: true });
      showToast(
        fields.name !== undefined
          ? t("switchDetailPage.nameUpdatedToast")
          : fields.location !== undefined
            ? t("switchDetailPage.locationUpdatedToast")
            : t("switchDetailPage.deviceUpdatedToast"),
      );
    },
    // In both cases: the server has the last word. It may have normalized
    // the input, or it may have refused it outright.
    onSettled: () => qc.invalidateQueries({ queryKey: ["devices", siteId] }),
  });
  const places = [...new Set((devices ?? []).map((d) => d.location).filter(Boolean))].sort();

  // The instance's addressing domains: which network this equipment lives
  // in, for declaration purposes. Especially useful for devices entered by
  // hand — those coming from discovery inherit the domain of their address
  // range.
  const { data: domains } = useQuery({
    queryKey: ["address-domains"],
    queryFn: () => get<AddressDomain[]>("/api/discovery/domains"),
  });
  const setDomain = useMutation({
    mutationFn: (id: string) =>
      put<Device>(`/api/devices/${deviceId}/address-domain`, { address_domain_id: id }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      showToast(t("switchDetailPage.domainUpdated"));
    },
  });

  /**
   * The proxies that COVER this site — the only ones capable of collecting
   * its equipment. This is intentionally a restricted list, not the full
   * inventory of proxies: attachment alone gives no coverage, so assigning
   * a proxy that doesn't cover the site wouldn't just move collection
   * elsewhere, it would stop it. The device would stay pinned — and
   * therefore green on screen — with nothing polling it over SNMP and no
   * recipient for its traps.
   *
   * The controller refuses this assignment on its own side too (a dropdown
   * is not an access control, and the API can be called directly); the
   * point here is not to offer a choice that would just be refused.
   */
  const { data: collectors } = useQuery({
    queryKey: ["collector-proxies", siteId],
    queryFn: () => get<ProxyInfo[]>(`/api/sites/${siteId}/collector-proxies`),
    enabled: !!siteId,
  });
  const setCollector = useMutation({
    mutationFn: (id: string) =>
      put<Device>(`/api/devices/${deviceId}/collector`, { proxy_id: id }),
    onSuccess: (fresh) => {
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      showToast(
        fresh.proxy_name
          ? t("switchDetailPage.collectorSnmpToast", { name: fresh.proxy_name })
          : t("switchDetailPage.collectorCoverageToast"),
      );
    },
    // The controller's refusal carries the full reasoning ("it would no
    // longer be collected by anyone"): it must reach the screen word for
    // word, not as a generic "failure" that would leave you wondering
    // which one.
    onError: (err) =>
      showToast(err instanceof Error ? err.message : t("switchDetailPage.attachmentRefusedToast")),
  });

  // THE FIVE WIRE-GESTURE MUTATIONS LIVE ABOVE THE EARLY RETURNS BELOW, AND
  // THAT PLACEMENT IS THE FIX FOR A BLANK DEVICE PAGE.
  //
  // They used to sit further down, after `if (!dev) return …`. That is a Rules
  // of Hooks violation with a delayed fuse: on the first render of a cold page
  // the devices query has not resolved, `dev` is undefined, the component
  // returns early and React records the hooks it saw; when the query lands, the
  // same component runs five `useMutation` hooks more. React refuses the
  // mismatch ("Rendered more hooks than during the previous render"), unmounts
  // the tree, and the whole sheet goes BLANK — no faceplate, no telemetry, and
  // no Console button, which is how it was reported.
  //
  // It only fires on a COLD mount — a pasted device link, a bookmark, or F5 on
  // this page. Arriving from the fleet list finds the devices query already in
  // cache, so `dev` is defined on the first render, no early return happens, and
  // every hook runs: the page works, which is why this survived so long.
  //
  // Nothing here reads a value declared between this point and where they used
  // to be: every reference (`dev?.name`, the mutation parameters) lives inside a
  // callback that runs on a click, long after render.
  // The IMMEDIATE half of a PoE gesture, offered only when the connector
  // declares PoE writable (dev.poe_caps.write). The stored intent keeps riding
  // the ports endpoint through mutatePort/mutatePorts exactly as before —
  // deliberately two calls, not one: a device that refuses the write must not
  // lose the recorded intention, and the auto-apply path stays enable-only
  // (cf. internal/api/poe_apply.go). Skips are surfaced, not swallowed:
  // "this jack does not deliver 24v" is the exact fact the operator needs.
  const applyPoE = useMutation({
    mutationFn: (ports: Record<string, string>) =>
      post<{
      ok: boolean;
      result?: { skipped?: string[]; not_applied?: { port?: string; kind: "guardrail" | "platform" | "device"; text: string }[]; notes?: string[] };
    }>(
        `/api/devices/${deviceId}/poe/apply`, { ports }),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      const skipped = [
        ...(res.result?.skipped ?? []),
        ...(res.result?.not_applied ?? []).map((n) => n.text),
      ];
      if (skipped.length > 0) {
        showToast(t("switchDetailPage.poeApplySkippedToast", {
          count: skipped.length, first: skipped[0],
        }));
      }
    },
    onError: (err) =>
      showToast(err instanceof Error ? err.message : t("switchDetailPage.poeApplyFailedToast")),
  });

  /**
   * SHUT / NO-SHUT one port, NOW.
   *
   * `enable` is what the contract carries — ENABLED, so `false` shuts — and it
   * comes from `adminGesture().enable`, never from a local negation: the
   * polarity inverts silently, and `{"GE5": true}` read as "disable GE5" cuts a
   * customer with a request that looks like it turned something on.
   *
   * `force` rides through untouched. It is the operator's answer to "this port
   * carries a live link", collected by an explicit checkbox in the overlay — a
   * caller that always sent true would make the confirmation decorative, which
   * is exactly what the connector-side guardrail expects us not to do.
   *
   * NOTHING IS STORED. The endpoint writes no intent (portadmin_apply.go says
   * why: a shut recorded as an intent would be re-shut on every reconciliation
   * pass), so the faceplate keeps showing the last READ link state until the
   * next ⟳ — the overlay says so rather than letting the operator wonder why the
   * square did not change colour.
   */
  const portAdmin = useMutation({
    mutationFn: (args: { port: string; enable: boolean; force: boolean }) =>
      post<{
      ok: boolean;
      result?: { skipped?: string[]; not_applied?: { port?: string; kind: "guardrail" | "platform" | "device"; text: string }[]; notes?: string[] };
    }>(
        `/api/devices/${deviceId}/ports/admin`,
        { ports: { [args.port]: args.enable }, force: args.force },
      ),
    onSuccess: (res, args) => {
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      const skipped = [
        ...(res.result?.skipped ?? []),
        ...(res.result?.not_applied ?? []).map((n) => n.text),
      ];
      // A refused port is an ORDINARY answer on this endpoint, not an error, and
      // its reason is written for a human: show it verbatim. Reducing it to
      // "failed" would hide the one sentence that says what to do next.
      if (skipped.length > 0) {
        showToast(t("switchDetailPage.portAdminSkippedToast", {
          port: args.port, reason: skipped[0],
        }));
      } else {
        showToast(t(args.enable
          ? "switchDetailPage.portAdminEnabledToast"
          : "switchDetailPage.portAdminShutToast", {
          port: args.port, device: dev?.name ?? "",
        }));
      }
      (res.result?.notes ?? []).forEach((n) => showToast(n));
      setActionPort(null);
    },
    // 422 here means "this platform does not declare port administrative state
    // as writable", composed server-side for the operator (cf.
    // driver.Unsupported). Word for word, never a generic failure.
    onError: (err) =>
      showToast(err instanceof Error ? err.message : t("switchDetailPage.portAdminFailedToast")),
  });

  /**
   * PoE POWER CYCLE — the volts go away and come back, and the far-end device
   * REBOOTS. Not a link bounce, and the overlay is built around that difference.
   *
   * # This request blocks for the whole off-duration
   *
   * The handler answers only once power is BACK, on purpose: the restore is the
   * contract, and a response that arrived early would leave no way to learn that
   * the second half failed — i.e. that a port is dark. So nothing here caps the
   * wait: no AbortController, no signal, no retry. `fetch` has no client-side
   * timeout of its own, which for once is what we want; the overlay shows a
   * pending state for the four seconds instead of pretending the click failed.
   *
   * react-query's default of zero retries matters more than usual on this one:
   * a retried power cycle would reboot the far end twice.
   */
  const powerCycle = useMutation({
    mutationFn: (args: { port: string; offForSeconds: number }) =>
      post<{
      ok: boolean;
      result?: { skipped?: string[]; not_applied?: { port?: string; kind: "guardrail" | "platform" | "device"; text: string }[]; notes?: string[] };
    }>(
        `/api/devices/${deviceId}/ports/power-cycle`,
        { ports: [args.port], off_for_seconds: args.offForSeconds },
      ),
    onSuccess: (res, args) => {
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      const skipped = [
        ...(res.result?.skipped ?? []),
        ...(res.result?.not_applied ?? []).map((n) => n.text),
      ];
      if (skipped.length > 0) {
        showToast(t("switchDetailPage.portCycleSkippedToast", {
          port: args.port, reason: skipped[0],
        }));
      } else {
        showToast(t("switchDetailPage.portCycleDoneToast", { port: args.port }));
      }
      // Notes on this path name what the connector could not put back — which
      // port is dark and what class it should be carrying. The single most
      // important sentence of the whole gesture, so it gets its own toast.
      (res.result?.notes ?? []).forEach((n) => showToast(n));
      setActionPort(null);
    },
    onError: (err) =>
      showToast(err instanceof Error ? err.message : t("switchDetailPage.portCycleFailedToast")),
  });

  /**
   * The IMMEDIATE half of a DHCP snooping gesture, sibling of applyPoE and
   * deliberately shaped the same way: the stored `dhcp_snooping` intent keeps
   * riding PUT /ports through mutatePort/mutatePorts, and this endpoint carries
   * only the wire write.
   *
   * Two calls and not one, for the reason poe_apply.go states: the automatic
   * path is ENABLE-ONLY (recon.RequestedExtras only reports positive intents),
   * so an untrust performed here is never silently re-trusted by a later
   * auto-apply — and a device that refuses the write does not cost the operator
   * the recorded decision.
   *
   * The bool is TRUSTED, taken at face value in both directions. VLANs are
   * resolved server-side from the site plan and are NEVER sent from here: a
   * caller naming its own list could install protection on a VLAN meshcraft does
   * not model, which no later pass would reconcile or remove.
   */
  const applySnoop = useMutation({
    mutationFn: (ports: Record<string, boolean>) =>
      post<{
      ok: boolean;
      result?: { skipped?: string[]; not_applied?: { port?: string; kind: "guardrail" | "platform" | "device"; text: string }[]; notes?: string[] };
    }>(
        `/api/devices/${deviceId}/dhcp-snooping/apply`, { ports }),
    onSuccess: (res, ports) => {
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      const skipped = [
        ...(res.result?.skipped ?? []),
        ...(res.result?.not_applied ?? []).map((n) => n.text),
      ];
      if (skipped.length > 0) {
        showToast(t("switchDetailPage.snoopSkippedToast", {
          count: skipped.length, first: skipped[0],
        }));
      } else {
        showToast(t("switchDetailPage.snoopAppliedToast", {
          count: Object.keys(ports).length,
        }));
      }
      // The connector may have turned the switch-wide feature on as a side
      // effect of this click, and it SAYS SO here. A UI that swallowed these
      // notes would show a trust flag on a device that inspects nothing.
      (res.result?.notes ?? []).forEach((n) => showToast(n));
    },
    onError: (err) =>
      showToast(err instanceof Error ? err.message : t("switchDetailPage.snoopApplyFailedToast")),
  });

  /**
   * PER-PORT STORM CONTROL, the wire half.
   *
   * The body carries a UNIT with every threshold, and the frontend never
   * supplies one the platform did not declare — buildStormBody refuses instead
   * (stormControl.ts). That is the whole safety property of this feature: a
   * number sent without its unit is ten percent of a gigabit on one switch and
   * ten packets a second on the next, and no connector converts between them.
   *
   * NOTHING IS STORED as intent, and there is no automatic sibling: no
   * reconciliation pass sets storm limiters, so a stored value would be one
   * nothing replays — and the first pass that learned to replay it would start
   * re-imposing thresholds an operator had deliberately removed by hand.
   */
  const applyStorm = useMutation({
    mutationFn: (body: { ports: Record<string, unknown[]> }) =>
      post<{
      ok: boolean;
      result?: { skipped?: string[]; not_applied?: { port?: string; kind: "guardrail" | "platform" | "device"; text: string }[]; notes?: string[] };
    }>(
        `/api/devices/${deviceId}/storm-control/apply`, body),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      const skipped = [
        ...(res.result?.skipped ?? []),
        ...(res.result?.not_applied ?? []).map((n) => n.text),
      ];
      if (skipped.length > 0) {
        // A refused CLASS lands here, not in the error: the platform wrote what
        // it could and named what it could not. Showing the first one keeps the
        // toast readable; the overlay lists them all.
        showToast(t("switchDetailPage.stormSkippedToast", {
          count: skipped.length, first: skipped[0],
        }));
      } else {
        showToast(t("switchDetailPage.stormAppliedToast"));
      }
      (res.result?.notes ?? []).forEach((n) => showToast(n));
    },
    onError: (err) =>
      showToast(err instanceof Error ? err.message : t("switchDetailPage.stormApplyFailedToast")),
  });

  if (error) return <ErrorMsg error={error} />;
  if (!dev) return <div className="empty">{t("switchDetailPage.notFound")}</div>;

  /**
   * An attachment can become invalid AFTER the fact: narrowing a proxy's
   * coverage, elsewhere in the product, leaves behind equipment attached
   * to a proxy that no longer covers it. The write-side refusal doesn't
   * prevent this existing case, and the result is the worst of all — no
   * one polls the equipment anymore, while it keeps answering pings and
   * shows green.
   *
   * We detect it by comparing the attachment to the SAME list used to make
   * the choice: two different sources would end up contradicting each
   * other. `collectors === undefined` = list not yet loaded: assert
   * nothing in that case, otherwise the banner flashes every time the
   * device sheet opens.
   */
  const covering = collectors ?? [];
  const collectorStranded =
    !!dev.proxy_id && collectors !== undefined && !covering.some((p) => p.id === dev.proxy_id);

  const off = dev.status === "off";
  const rates = deviceRates(dev);
  const ports = expandPorts(dev);
  // Range is measured on PHYSICAL order of ports, not rows
  // displayed: a facade ≥16 ports is odd/even interleaved, and “from 1 to
  // 8" should take 1…8, not every other row.
  const selSet = new Set(selPorts);
  const selected = ports.filter((p) => selSet.has(p.name));
  const sel = selected.length === 1 ? selected[0] : null;

  const clickPort = (
    name: string,
    e: { shiftKey: boolean; ctrlKey: boolean; metaKey: boolean; detail?: number },
  ) => {
    const add = e.ctrlKey || e.metaKey;
    setSelProf(null);
    // A physical port takes the side panel back from an aggregate. The two
    // selections never coexist, so the panel always describes the thing the
    // next write applies to.
    setSelLag(null);
    /**
     * DOUBLE-CLICK OPENS THE ACTION OVERLAY, and the single click that preceded
     * it is honoured rather than undone.
     *
     * The browser delivers a double-click as two `click` events, the second
     * carrying `detail === 2`. We deliberately do NOT wait on a timer to tell
     * the two apart: a 250 ms delay before a port gets selected would make the
     * whole facade feel unresponsive, on the one screen where an operator clicks
     * dozens of squares in a row.
     *
     * So the first click has already selected this port alone, and this branch
     * only re-asserts that state before opening the overlay. The resulting
     * selection is therefore always "exactly the port named in the overlay" —
     * the one state that cannot mislead: whatever the operator had selected
     * before, the panel behind the overlay describes the port they are about to
     * shut, not a range they are no longer looking at.
     *
     * Modifiers are excluded on purpose. ⇧ and Ctrl/⌘ compose a SELECTION, and a
     * fast second modified click is an ordinary part of doing that — opening a
     * destructive confirmation there would punish operators for clicking
     * quickly.
     */
    if (!e.shiftKey && !add && (e.detail ?? 1) >= 2) {
      setSelPorts([name]);
      setAnchor(name);
      setActionPort(name);
      return;
    }
    if (e.shiftKey && anchor) {
      const a = ports.findIndex((p) => p.name === anchor);
      const b = ports.findIndex((p) => p.name === name);
      if (a >= 0 && b >= 0) {
        const range = ports.slice(Math.min(a, b), Math.max(a, b) + 1).map((p) => p.name);
        // ⇧ alone replaces the selection, ⇧+Ctrl adds the range to the existing one:
        // this is what allows you to compose a range WITH isolated ports.
        setSelPorts(add ? [...new Set([...selPorts, ...range])] : range);
        return;
      }
    }
    if (add) {
      setSelPorts(selSet.has(name) ? selPorts.filter((n) => n !== name) : [...selPorts, name]);
      setAnchor(name);
      return;
    }
    setSelPorts([name]);
    setAnchor(name);
  };

  /**
   * SELECTING AN AGGREGATE (ADR-0007, decision 1) — and the range refusal
   * that comes with it.
   *
   * Selecting clears the port selection AND the range anchor. Clearing the
   * anchor is the load-bearing half: it is what makes a later ⇧-click on a
   * physical port an ordinary selection instead of a range measured from
   * wherever the operator happened to be before they clicked an aggregate.
   */
  const clickLag = (name: string) => {
    setSelPorts([]);
    setAnchor(null);
    setSelProf(null);
    setSelLag(name);
  };

  /**
   * A ⇧ / Ctrl-⌘ CLICK LANDED ON AN AGGREGATE, AND IT IS REFUSED OUT LOUD.
   *
   * Range selection is measured on PHYSICAL order (see FacadeBlockRows, and
   * the wrapping that once made ⇧-click return a set that looked wrong and
   * was right). "From port 12 to LAG2" names no set at all: no physical order
   * contains both. The ADR requires a refusal rather than a reinterpretation,
   * and a toast is how this page already says no — because the alternative,
   * quietly treating it as a plain click, hands back a selection the operator
   * did not ask for on the screen where the selection is what the next write
   * applies to.
   */
  const refuseLagRange = () => showToast(t("lag.rangeRefused"));

  /**
   * CLICKING THE PAGE BACKGROUND CLEARS THE SELECTION — the pointer equivalent of
   * Escape, and the same gesture as the multi-panel's "Clear selection" button.
   *
   * Bound on `<main>` and not on the document, which is what makes the side panel
   * safe for free: the panel is a SIBLING of `<main>`, so a click on a VLAN
   * dropdown never reaches this handler at all. An operator half-way through
   * adjusting a PVID on twelve ports cannot lose the twelve ports by clicking
   * the control they came for.
   *
   * Inside `<main>`, INTERACTIVE TARGETS are excluded by walking up from the
   * event target. Two reasons, and the first is not cosmetic:
   *
   *   - a PortSquare IS a `<button>`. Its own onClick has just set the
   *     selection, and this handler runs afterwards on the same click — without
   *     the exclusion, clicking a port would select it and immediately clear it,
   *     i.e. the facade would stop working entirely;
   *   - every other control under `<main>` (the re-read button, the console
   *     button, banner actions, the telemetry window picker, the help bubbles)
   *     is something the operator pressed ON PURPOSE while holding a selection.
   *
   * `.helptip` covers the bubble as well as its button: those are rendered
   * `position: fixed` outside this subtree, so they would not reach us anyway —
   * listed for the day one is inlined.
   */
  const clearOnBackground = (e: React.MouseEvent) => {
    if (selPorts.length === 0 && !selProf && !selLag) return;
    const el = e.target as HTMLElement | null;
    if (el?.closest?.("button, a, input, select, textarea, label, summary, details, .helptip")) {
      return;
    }
    setSelPorts([]);
    setSelProf(null);
    setSelLag(null);
  };

  const poePct = dev.poe_budget_w
    ? Math.min(100, Math.round((rates.poe / dev.poe_budget_w) * 100))
    : 0;

  // The faceplate's physical blocks and rows (facade.ts).
  const facade = buildFacade(dev.port_groups, ports);

  // The one merge every port write goes through: patch OVER the port's own
  // intent, so a VLAN edit never erases its label or PoE, and a nested stp
  // patch never erases the stp keys it does not name.
  const mergeIntent = (base: PortIntent, patchIn: Partial<PortIntent>): PortIntent => {
    const next = { ...base, ...patchIn };
    if (patchIn.stp) next.stp = { ...base.stp, ...patchIn.stp };
    return next;
  };
  // Apply to ALL selection: patch merges with intent
  // specific to each port, otherwise configuring the VLAN of a range would erase
  // passing the labels and the PoE of each.
  const mutatePorts = (patch: Partial<PortIntent>, msg?: string) => {
    if (selected.length === 0) return;
    if (selected.length === 1) {
      setPort.mutate({ port: selected[0].name, intent: mergeIntent(selected[0].p, patch) });
    } else {
      const body: Record<string, PortIntent> = {};
      selected.forEach((pr) => {
        // PoE cannot be invented on a port that does not have one: the selection
        // can mix copper PoE and SFP, and force poe=true on an SFP
        // would write an intention that the material can never satisfy.
        const eff = patch.poe && !pr.poeCap ? { ...patch, poe: false } : patch;
        body[pr.name] = mergeIntent(pr.p, eff);
      });
      setPorts.mutate(body);
    }
    if (msg) showToast(msg);
  };
  // PER-PORT patches in one gesture: the multi panel's PoE repair re-aligns
  // each port to ITS OWN group, so the value differs port by port — a single
  // shared patch cannot say that. Same batch endpoint as mutatePorts: one
  // request, one apply, one log line. Ports outside the selection are ignored
  // on purpose — the gesture is scoped to what is on screen.
  const mutatePortsEach = (patches: Record<string, Partial<PortIntent>>, msg?: string) => {
    const body: Record<string, PortIntent> = {};
    selected.forEach((pr) => {
      const patchIn = patches[pr.name];
      if (patchIn) body[pr.name] = mergeIntent(pr.p, patchIn);
    });
    if (Object.keys(body).length === 0) return;
    setPorts.mutate(body);
    if (msg) showToast(msg);
  };
  const mutatePort = (pr: PortRef, patch: Partial<PortIntent>, msg?: string) => {
    setPort.mutate({ port: pr.name, intent: mergeIntent(pr.p, patch) });
    if (msg) showToast(msg);
  };

  // THE PORT BEING ACTED ON BLINKS, with the same `port-applying` fade an apply
  // uses — and deliberately NOT through the drift pipeline that drives it there.
  //
  // `dev.applying` + `dev.drift` is VLAN reconciliation: a stored intent, a
  // computed divergence, a write that closes it. Shutting a port is an ACTION
  // (cf. internal/driver/portadmin.go) — nothing is stored in devices.ports and
  // nothing diverges, so the port appears in no `dev.drift` entry and the server
  // pipeline has nothing to say about it. Feeding it through there would mean
  // fabricating a drift entry for a state we deliberately do not reconcile, and
  // the next reconciliation pass would set about "fixing" it.
  //
  // So: shared animation, local source. While the mutation is in flight we know
  // exactly which socket is being written, which is all the faceplate needs — and
  // the operator watches the socket they clicked, not the toast. It stays here,
  // after the rest of the map, because it reads `portAdmin` and `powerCycle` —
  // which are now declared above the early returns, so this is no longer the
  // temporal-dead-zone constraint it once was, merely the order that keeps the
  // map's construction in one place.
  if (actionPort && (portAdmin.isPending || powerCycle.isPending)) {
    driftPorts.set(actionPort, "applying");
  }

  // NO HOSTNAME MUTATION HERE, and that is the design.
  //
  // The equipment's own name used to be pushed by a button on this page
  // (POST /api/devices/{id}/hostname/apply, now deleted). It is RECONCILED
  // instead (internal/driver/hostname.go): renaming the device through
  // `saveDevice` below is what writes the name onto the equipment, and a
  // backend loop repairs drift on its own with a per-device cooldown.
  //
  // Nothing replaces the button. A "repair now" control next to an automatic
  // repair teaches that the automation is not trusted, and it would spend a
  // session on ranges that grant five of them to do what the loop was about to
  // do for free. The panel reports the verdict (`hostname_conformity`) and
  // stops there.

  const applyProfile = () => {
    if (selected.length === 0 || !selProf) return;
    const prof = (profiles ?? []).find((p) => p.id === selProf);
    if (!prof) return;
    const spec = prof.spec as unknown as PortProfileSpec;
    if (spec.eng_only && role !== "ing") return;
    const stp: PortIntent["stp"] = {};
    if (typeof spec.stp_edge === "boolean") {
      stp.edge = spec.stp_edge ? "yes" : "auto";
    }
    if (typeof spec.stp_enabled === "boolean") {
      stp.enabled = spec.stp_enabled;
    }
    if (typeof spec.bpdu_guard === "boolean") {
      stp.bpdu_guard = spec.bpdu_guard;
    }
    mutatePorts(
      {
        mode: spec.mode,
        pvid: spec.pvid ?? null,
        tagged: spec.mode === "trunk" ? (spec.tagged ?? []) : [],
        poe: !!spec.poe,
        label: prof.name,
        // The port now FOLLOWS this group: the reference is what lets the
        // panel open in group mode next time, and what the PoE override
        // indicator compares against. Values above are still STAMPED — a
        // later profile edit does not ripple (cf. docs/en/02-Frontend/Profiles
        // and groups: no inheritance), the reference only records provenance.
        profile_id: prof.id,
        ...(Object.keys(stp).length > 0 ? { stp } : {}),
      },
      t("switchDetailPage.profileAppliedToast", { name: prof.name, ports: selected.length === 1 ? selected[0].name : t("switchDetailPage.portsCountLabel", { count: selected.length }), device: dev.name }),
    );
    setSelProf(null);
  };

  const countOnSwitch = (vid: number) => {
    let n = 0;
    ports.forEach((pr) => {
      if (pr.p.mode && pr.p.mode !== "libre" && serves(pr.p, vid)) n++;
    });
    return n;
  };
  const vlansOnSwitch = (vlans ?? []).filter((v) => countOnSwitch(v.vlan_id) > 0);

  return (
    <div className="page2col wide-panel">
      <main onClick={clearOnBackground}>
        <div className="row" style={{ justifyContent: "space-between" }}>
          <ActionButton kind="close" className="ghost" onClick={() => navigate(sitePath(siteSlug, "equipment"))}>
            {t("switchDetailPage.backToDevices")}
          </ActionButton>
          <span className="row" style={{ gap: 8 }}>
            <span className="note">{syncLabel(dev)}</span>
            {/* HOW OLD the verdict to its left is. `syncLabel` says whether the
                device matches its intent; this says when anyone last checked —
                two questions the sheet used to answer with one sentence, which
                is why "is this live or from an hour ago?" had no answer short
                of pressing ⟳ and watching what moved. */}
            <ConfigFreshness dev={dev} />
            {/* CONSOLE: an interactive shell, or the vendor UI when the platform
                has no SSH. site_admin only, and the controller decides which of
                the two — the button just asks. */}
            {role === "ing" && (
              <QuickTip text={t("console.openTitle")} className="quicktip-inline">
                <ActionButton
                  kind="terminal"
                  className="ghost"
                  onClick={() => setConsoleOpen(true)}
                >
                  {t("console.openBtn")}
                </ActionButton>
              </QuickTip>
            )}
            <QuickTip text={t("switchDetailPage.rereadTitle")} className="quicktip-inline">
              <button
                className="ghost icon-btn"
                disabled={readState.isPending}
                aria-label={t("switchDetailPage.rereadAria")}
                onClick={() => readState.mutate()}
              >
                {/* Label lives in tooltip and aria-label: icon alone is enough for a
                    secondary action, leaving room for status text that must stay readable. */}
                <span className={readState.isPending ? "spin" : undefined}>⟳</span>
              </button>
            </QuickTip>
          </span>
        </div>
        {consoleOpen && (
          <Suspense fallback={null}>
            <DeviceConsole
              deviceId={deviceId}
              deviceName={dev.name}
              siteId={siteId}
              onClose={() => setConsoleOpen(false)}
            />
          </Suspense>
        )}
        <DeviceBanners
          dev={dev}
          canEdit={role === "ing"}
          off={off}
          collectorStranded={collectorStranded}
          busy={applyCfg.isPending || importCfg.isPending}
          onApply={(force) => applyCfg.mutate(force)}
          onImport={() => importCfg.mutate()}
          onToast={showToast}
          onViewMgmtPlan={() => navigate(sitePath(siteSlug, "config", "management"))}
        />
        <DeviceHeader
          dev={dev}
          canEdit={role === "ing"}
          off={off}
          rates={rates}
          poePct={poePct}
          onRename={(name) => saveDevice.mutate({ name })}
        />
        <hr className="hr" style={{ margin: "12px 0 16px" }} />

        {off && (
          <div className="alert-banner">
            <span className="dot" style={{ background: "var(--danger)" }} />
            {t("switchDetailPage.unreachable")}{dev.status_note ? " — " + dev.status_note : t("switchDetailPage.lastKnownState")}
          </div>
        )}

        <Faceplate
          blocks={facade}
          profileNames={profileNames}
          vm={vm}
          off={off}
          hoverVlan={hoverVlan}
          selected={selSet}
          emphasis={lagEmphasis}
          drift={driftPorts}
          lagOf={lagMembers}
          onPortClick={clickPort}
          lags={deviceLags}
          lagsObservedOnly={deviceLagsAll.observedOnly}
          selectedLag={selLag}
          canAddLag={role === "ing" && !!dev.lag_caps?.write}
          onLagSelect={clickLag}
          onLagRangeRefused={refuseLagRange}
          onLagAdd={() => setLagEditing("")}
          onPortHover={(name, e) => {
            claimHoverTip(facePortTipId);
            setPortHover({
              key: name, deviceId, port: name,
              title: dev.name + " · " + name,
              // The SAME lines the square's own tip carries, from the same
              // function — but these are the ones the operator actually sees:
              // claiming the hover slot right above dismisses that bubble.
              facts: portConfigLines(
                ports.find((x) => x.name === name)?.p ?? {},
                vm,
                profileNames,
                t,
              ),
              subtitle: t("planPage.linkTraffic24h") as string,
              classCaps: dev.traffic_class_caps,
              clientX: e.clientX, clientY: e.clientY,
            });
          }}
          onPortHoverMove={(name, e) =>
            setPortHover((cur) =>
              cur && cur.key === name
                ? { ...cur, clientX: e.clientX, clientY: e.clientY }
                : cur)
          }
          onPortHoverLeave={(name) =>
            setPortHover((cur) => {
              if (cur?.key !== name) return cur;
              releaseHoverTip(facePortTipId);
              return null;
            })
          }
        />

        {/* TELEMETRY under the facade: what port boxes cannot carry. Here, not in
            the right panel, because these values describe the DEVICE not the
            selected port — putting them on the right would hide them as soon as a
            socket is clicked. */}
        {/* THE AGGREGATES CARD IS GONE (ADR-0007). It was a permanent block
            that said "no aggregate on this device" on the majority of boxes,
            and it treated as an encart the thing the data model treats as an
            interface. The aggregates now live ON the facade, in their own
            labelled block, and their diagnostic lives in the side panel the
            way a port's does. */}
        <TelemetryPanel
          dev={dev}
          tel={tele}
          minutes={teleMinutes}
          onMinutes={setTeleMinutes}
          loading={teleFetching}
        />
      </main>

      <aside className="side-panel">
        {/* AN AGGREGATE OWNS THE PANEL FIRST, and it can only be reached when
            no port is selected (clickLag clears them). Listed before the port
            branches rather than after, so the exclusivity is readable here
            and not only in the handlers. */}
        {selLagObj ? (
          <LagSidePanel
            lag={selLagObj}
            imported={deviceLagsAll.observedOnly.has(selLagObj.name)}
            canWrite={role === "ing" && !!dev.lag_caps?.write}
            busy={applyLags.isPending}
            onEdit={() => setLagEditing(selLagObj.name)}
            onDissolve={() =>
              applyLags.mutate(deviceLags.filter((l) => l.name !== selLagObj.name))
            }
            onClear={() => setSelLag(null)}
          />
        ) : selected.length > 1 ? (
          <MultiPortPanel
            // Keyed by the selection, same reason as the single-port panel:
            // the group/manual selector is per-selection UI state, and
            // carrying it from one selection to the next would open the
            // wrong lane on ports that live by the other one.
            key={selected.map((p) => p.name).join("|")}
            dev={dev}
            sel={selected}
            all={ports}
            role={role}
            off={off}
            vlans={vlans ?? []}
            profiles={profiles ?? []}
            selProf={selProf}
            onSelProf={setSelProf}
            onApply={applyProfile}
            onPatch={mutatePorts}
            onPatchEach={mutatePortsEach}
            onPoEWrite={(ports) => applyPoE.mutate(ports)}
            onSnoopWrite={(ports) => applySnoop.mutate(ports)}
            snoopWriting={applySnoop.isPending}
            onClear={() => { setSelPorts([]); setSelProf(null); }}
          />
        ) : !sel ? (
          <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
            <SidePanelGroup title={t("switchDetailPage.switchSummary")} defaultOpen>
              <div className="kv" style={{ marginTop: 2 }}>
                <span className="k">{t("switchDetailPage.stateLabel")}</span>
                <span style={{ display: "flex", flexWrap: "wrap", gap: 8, alignItems: "center" }}>
                  <ProtocolPills dev={dev} />
                  {/* THE REACHABILITY SENTENCE CARRIES NO COLOUR (2026-08-09,
                      UI-GUIDE §7). It used to be painted `statusColor(dev.status)`,
                      and that function is handed the STATUS ALONE — it cannot see
                      `status_at`, so it cannot tell the four states `deviceDot`
                      arbitrates apart, and it renders never-observed and blind
                      identically. `DeviceHeader` on this same page already draws
                      `<StatusDot dev={dev} />`, which does have the date and does
                      make the distinction, so the two disagreed on one screen: a
                      pulsing orange dot at the top, a plain green sentence below
                      it. Weight carries the emphasis and the adjacent dot carries
                      the colour, which is what `dashboardTiles.tsx` already did. */}
                  {/* THE CONNECTOR'S SENTENCE MOVED TO THE HOVER, 2026-10-24.
                      The row read "UNREACHABLE — for 4 h — no longer responds
                      to ping (proxy-lab), which last confirmed its identity
                      68 days ago": four clauses wrapping over three lines in a
                      300px panel, of which only the first two were ever scanned.

                      This is NOT the `?` treatment the guide forbids for an
                      observation. QuickTip is the primitive UI-GUIDE §3 names
                      for exactly this — "observation, detail, reason", read
                      every time, no delay. What stays on the row is the state
                      and how long it has held; what moves is the prose. The
                      tip itself is structured rather than narrated: the
                      collector that last saw the device, and when. */}
                  <QuickTip text={reachDetail(dev, t)} className="quicktip-inline">
                  <span style={{ fontWeight: 700 }}>
                    {reachability(dev).label}
                    {dev.status_at ? (
                      <span style={{ fontWeight: 400, color: "var(--text-dim)" }}>
                        {" — " + sinceLabel(dev.status_at)}
                      </span>
                    ) : null}
                  </span>
                  </QuickTip>
                </span>
                <span className="k">{t("switchDetailPage.placeLabel")}</span>
                {role === "ing" ? (
                  <PillPicker
                    value={dev.location}
                    options={places}
                    placeholder={t("switchDetailPage.placePlaceholder")}
                    disabled={saveDevice.isPending}
                    onChange={(v) => saveDevice.mutate({ location: v })}
                  />
                ) : (
                  <span>{dev.location || "—"}</span>
                )}
                <AddressDomainRow
                  domains={domains}
                  value={dev.address_domain_id}
                  canEdit={role === "ing"}
                  busy={setDomain.isPending}
                  onChange={(id) => setDomain.mutate(id)}
                />
                <span className="k" style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
                  {t("switchDetailPage.addressLabel")}
                  <HelpTip label={t("switchDetailPage.addressHelpLabel")}>
                    <p>{t("switchDetailPage.addressHelp1")}</p>
                    <p>{t("switchDetailPage.addressHelp2")}</p>
                    <p>{t("switchDetailPage.addressHelp3")}</p>
                  </HelpTip>
                </span>
                {role === "ing" ? (
                  <input
                    key={dev.id + ":ip"}
                    defaultValue={dev.mgmt_ip}
                    placeholder="10.0.0.1"
                    style={{ minHeight: 28, fontSize: 12 }}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") (e.target as HTMLInputElement).blur();
                    }}
                    onBlur={(e) => {
                      if (e.target.value !== dev.mgmt_ip)
                        saveDevice.mutate({ mgmt_ip: e.target.value });
                    }}
                  />
                ) : (
                  <span>{dev.mgmt_ip || "—"}</span>
                )}
                <HostnameRow conf={dev.hostname_conformity} />
                <CollectorRow
                  dev={dev}
                  covering={covering}
                  stranded={collectorStranded}
                  canEdit={role === "ing"}
                  busy={setCollector.isPending}
                  onChange={(id) => setCollector.mutate(id)}
                />
                <span className="k">{t("switchDetailPage.systemLabel")}</span><span>{dev.extra?.os || dev.vendor || "—"}</span>
                <span className="k">{t("switchDetailPage.uptimeLabel")}</span><span>{dev.extra?.uptime || "—"}</span>
              </div>
              {dev.poe_budget_w > 0 && (
                <div style={{ marginTop: 12 }}>
                  <div className="row" style={{ justifyContent: "space-between", fontSize: 11 }}>
                    <span className="kicker">{t("switchDetailPage.poeBudget")}</span>
                    <span className="note">{Math.round(rates.poe)} / {dev.poe_budget_w} W</span>
                  </div>
                  <div className="meter">
                    <div
                      style={{
                        width: poePct + "%",
                        background: poePct >= 85 ? "var(--warn)" : "var(--ok)",
                      }}
                    />
                  </div>
                </div>
              )}
            </SidePanelGroup>

            {/* Management addressing just UNDER identity: the address is already
                above, and "how did it get it?" comes up next. Collapsed by
                default — the card is tall; expand it when changing addressing. */}
            <SidePanelGroup
              title={t("switchDetailPage.mgmtNetwork")}
              meta={mgmtEffectiveMeta(dev, t)}
              defaultOpen={false}
            >
              <MgmtAddressingCard
                dev={dev}
                canEdit={role === "ing"}
                onToast={showToast}
                embedded
                vlans={vlans ?? []}
              />
            </SidePanelGroup>

            <SidePanelGroup
              title={t("switchDetailPage.stpTitle")}
              meta={stpEffectiveMeta(dev, t)}
              defaultOpen={false}
            >
              <SpanningTreeCard
                dev={dev}
                canEdit={role === "ing"}
                onToast={showToast}
              />
            </SidePanelGroup>

            <SidePanelGroup
              title={t("switchDetailPage.tagsTitle")}
              defaultOpen
              meta={
                <HelpTip label={t("switchDetailPage.tagsHelpLabel")}>
                  <p>{t("switchDetailPage.tagsHelp1")}</p>
                  <p>{t("switchDetailPage.tagsHelpTechnical")}</p>
                  <p>{t("switchDetailPage.tagsHelpRule")}</p>
                </HelpTip>
              }
            >
              <DeviceTags
                deviceId={dev.id}
                siteId={dev.site_id}
                canEdit={role === "ing"}
                onToast={showToast}
                embedded
              />
            </SidePanelGroup>

            <SidePanelGroup
              title={t("netui.vlanPlan")}
              meta={
                vlansOnSwitch.length > 0
                  ? t("switchDetailPage.vlanOnSwitchCount", { count: vlansOnSwitch.length })
                  : undefined
              }
              defaultOpen={false}
            >
              <VlanLegend
                vlans={vlansOnSwitch}
                countOf={countOnSwitch}
                onHover={setHoverVlan}
                embedded
              />
            </SidePanelGroup>
          </div>
        ) : (
          <PortPanel
            // Keyed by port: the group/manual selector is per-port UI state,
            // and carrying it from one socket to the next would open the wrong
            // tab on a port that follows nothing.
            key={sel.name}
            dev={dev}
            pr={sel}
            role={role}
            off={off}
            vlans={vlans ?? []}
            profiles={profiles ?? []}
            traffic={portTraffic(tele, sel.name)}
            classes={portClasses(tele, sel.name, dev.traffic_class_caps)}
            levels={portLevels(tele, sel.name)}
            stepSeconds={tele?.step_seconds ?? 0}
            selProf={selProf}
            onSelProf={setSelProf}
            onApply={applyProfile}
            onPatch={(patch, msg) => mutatePort(sel, patch, msg)}
            onPoEWrite={(ports) => applyPoE.mutate(ports)}
            onSnoopWrite={(ports) => applySnoop.mutate(ports)}
            snoopWriting={applySnoop.isPending}
          />
        )}
        <ErrorMsg error={setPort.error} />
        <ErrorMsg error={setPorts.error} />
      </aside>
      {/* The action overlay is a SIBLING of main and aside, not a child of the
          faceplate: it is `position: fixed`, and mounting it inside `<main>`
          would hand its backdrop clicks to the background-clears-selection
          handler — the operator would close the overlay and lose the port it
          named at the same time.

          Mounted only while a port is named, and keyed by that port, so the
          force checkbox can never survive from one port to the next: a ticked
          "shut it anyway" inherited by the next double-click is the exact
          accident this confirmation exists to prevent. */}
      {actionPort && ports.some((p) => p.name === actionPort) && (
        <PortActionOverlay
          key={actionPort}
          dev={dev}
          pr={ports.find((p) => p.name === actionPort)!}
          traffic={portTraffic(tele, actionPort)}
          classes={portClasses(tele, actionPort, dev.traffic_class_caps)}
          levels={portLevels(tele, actionPort)}
          stepSeconds={tele?.step_seconds ?? 0}
          adminBusy={portAdmin.isPending}
          cycleBusy={powerCycle.isPending}
          error={portAdmin.error ?? powerCycle.error}
          deviceId={deviceId}
          stormBusy={applyStorm.isPending}
          stormError={applyStorm.error}
          stormResult={applyStorm.data?.result}
          onStorm={(body) => applyStorm.mutate(body)}
          onAdmin={(enable, force) =>
            portAdmin.mutate({ port: actionPort, enable, force })
          }
          onPowerCycle={() =>
            powerCycle.mutate({ port: actionPort, offForSeconds: POWER_CYCLE_OFF_SECONDS })
          }
          onClose={() => {
            portAdmin.reset();
            powerCycle.reset();
            applyStorm.reset();
            setActionPort(null);
          }}
        />
      )}
      {/* THE AGGREGATE BUILDER, a sibling for the same reason the action
          overlay is one: it darkens the page, so its backdrop must not reach
          the background-clears-selection handler inside `<main>`.

          Keyed by what it edits, so the draft can never survive from one
          aggregate to the next — an operator who cancels an edit of LAG2 and
          opens LAG3 must not find LAG2's members staged under LAG3's name. */}
      {lagEditing !== null && (
        <LagBuilder
          key={lagEditing || "new"}
          blocks={facade}
          vm={vm}
          caps={dev.lag_caps}
          lags={deviceLags}
          editing={deviceLags.find((l) => l.name === lagEditing) ?? null}
          busy={applyLags.isPending}
          error={applyLags.error}
          onApply={(draft) =>
            // The COMPLETE list: the draft over everything else, because the
            // API replaces (an aggregate missing from the body is dissolved).
            applyLags.mutate([
              ...deviceLags.filter((l) => l.name !== (lagEditing || draft.name)),
              draft,
            ])
          }
          onClose={() => {
            applyLags.reset();
            setLagEditing(null);
          }}
        />
      )}
      <PortHoverTip hover={portHover} />
      {toast}
    </div>
  );
}

/**
 * What the reachability row says ON HOVER: the collector that last saw this
 * device and the moment it did, as two facts rather than a sentence.
 *
 * The connector's own `status_note` is appended when there is one, because
 * it is the only place a platform-specific reason ever appears — but it
 * comes last, after the two fields an operator actually acts on.
 */
function reachDetail(
  dev: Device,
  t: (key: string, opts?: Record<string, unknown>) => string,
): string {
  const bits: string[] = [];
  if (dev.proxy_name) bits.push(dev.proxy_name);
  if (dev.status_at) bits.push((viewerDate(dev.status_at) ?? new Date(dev.status_at)).toLocaleString());
  else bits.push(t("status.neverObserved"));
  const head = bits.join(" \u00b7 ");
  return dev.status_note ? head + "\n" + dev.status_note : head;
}

function mgmtEffectiveMeta(
  dev: Device,
  t: (key: string) => string,
): string | undefined {
  const effective = dev.mgmt_addressing?.effective;
  if (effective === "static") return t("mgmtAddressing.effectiveStatic");
  if (effective === "dhcp") return t("mgmtAddressing.effectiveDhcp");
  if (effective === "") return t("mgmtAddressing.effectiveUnknown");
  return undefined;
}

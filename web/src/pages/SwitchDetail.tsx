import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Suspense, lazy, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams } from "react-router-dom";
import { decodeDevicePathSegment, sitePath } from "../paths";
import { useSite, useSiteId } from "../useSite";
import {
  useSiteLive,
  del,
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
import { ConformMgmtButton } from "./ConformMgmt";
// LAZY on purpose: DeviceConsole pulls in xterm.js, a terminal emulator worth a
// quarter of a megabyte that the overwhelming majority of visits to this page
// never open. Loading it on demand keeps the switch sheet's first paint free of
// it, and the chunk arrives while the controller is still being asked what the
// device offers.
const DeviceConsole = lazy(() => import("./DeviceConsole"));
import {
  ActionButton,
  DriftBanner,
  FailedApplyBanner,
  IncompleteApplyBanner,
  PillPicker,
  SkippedBanner,
  UnsupportedBanner,
  ConnectorNotesBanner,
  ErrorMsg,
  HelpTip,
  QuickTip,
  TagInput,
  isRuleTag,
  isTechnicalTag,
  useToast,
  type DeviceTag,
} from "../components";
import {
  deviceRates,
  expandPorts,
  fmtMbps,
  reachability,
  serves,
  statusColor,
  syncLabel,
  vlanMap,
  type PortRef,
} from "../network";
import { PortSquare, ProtocolPills, StatusDot, VlanLegend } from "../netui";
import { portTraffic, TelemetryPanel, useTelemetry } from "../telemetry";
import { LineChart, SERIES } from "../viz";

const PORT_SIZE = 38;

export default function SwitchDetail() {
  const { t, i18n } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const { deviceName: deviceNameParam } = useParams();
  const decodedName = decodeDevicePathSegment(deviceNameParam ?? "");
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
  // Real-time: this is the sheet where we track a configuration being
  // applied. The state changes timing WITHOUT any user request —
  // automatic provisioning finishes several seconds after the last click
  // — and it's the controller that comes to announce it (see
  // emitDeviceBG in autoapply.go).
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
  const { data: profiles } = useQuery({
    queryKey: ["profiles", "port", siteId],
    queryFn: () => get<Profile[]>(`/api/sites/${siteId}/profiles/port`),
    enabled: !!siteId,
  });
  const dev = (devices ?? []).find((d) => d.name === decodedName);
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

  /**
   * The ports ACTUALLY affected by a drift, and nothing else. Flashing the
   * whole faceplate for the wrong port would send you searching for a
   * long time to find what's actually wrong.
   */
  const driftPorts = new Map<string, "pending" | "drift" | "applying">();
  if (dev?.sync === "pending" || dev?.sync === "drift") {
    for (const c of dev.drift ?? []) driftPorts.set(c.port, dev.sync);
  }
  // While an apply is actually running, the ports being written blink as
  // "provisioning" — a stronger, distinct animation from a still pending. The
  // ports in flight are exactly those the model diverges on (dev.drift).
  if (dev?.applying) {
    for (const c of dev.drift ?? []) driftPorts.set(c.port, "applying");
  }

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
      result?: { skipped?: string[] };
    }>(`/api/devices/${deviceId}/config/apply`, { force: !!force }),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      if (!res.ok) return showToast(res.error ?? t("switchDetailPage.configNotAppliedToast"));
      const restant = res.drift?.length ?? 0;
      const ecarte = res.result?.skipped?.length ?? 0;
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
  // contain a slash (FS « ethernet 1/11 », Cisco « Gi1/0/1 »); in the path
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
      return { previous };
    },
    onError: (err, _fields, ctx) => {
      // Without this rollback, the screen would indefinitely keep a value
      // that the controller refused — the worst of both worlds.
      if (ctx?.previous) qc.setQueryData(["devices", siteId], ctx.previous);
      showToast(err instanceof Error ? err.message : t("switchDetailPage.updateFailedToast"));
    },
    onSuccess: (_out, fields) => {
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

  const clickPort = (name: string, e: { shiftKey: boolean; ctrlKey: boolean; metaKey: boolean }) => {
    const add = e.ctrlKey || e.metaKey;
    setSelProf(null);
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
  const poePct = dev.poe_budget_w
    ? Math.min(100, Math.round((rates.poe / dev.poe_budget_w) * 100))
    : 0;

  // Groups the facade by physical block; ≥16 ports → 2 rows interleaved
  // (odd above / even below) as on real hardware.
  const facade: { label: string; rows: PortRef[][] }[] = [];
  {
    let idx = 0;
    for (const g of dev.port_groups ?? []) {
      const block = ports.slice(idx, idx + g.count);
      idx += g.count;
      let rows: PortRef[][];
      if (g.count >= 16) {
        const a: PortRef[] = [], b: PortRef[] = [];
        block.forEach((c, i) => (i % 2 ? b : a).push(c));
        rows = [a, b];
      } else rows = [block];
      facade.push({ label: g.label ?? g.prefix, rows });
    }
  }

  // Apply to ALL selection: patch merges with intent
  // specific to each port, otherwise configuring the VLAN of a range would erase
  // passing the labels and the PoE of each.
  const mutatePorts = (patch: Partial<PortIntent>, msg?: string) => {
    if (selected.length === 0) return;
    const merge = (base: PortIntent, patchIn: Partial<PortIntent>): PortIntent => {
      const next = { ...base, ...patchIn };
      if (patchIn.stp) next.stp = { ...base.stp, ...patchIn.stp };
      return next;
    };
    if (selected.length === 1) {
      setPort.mutate({ port: selected[0].name, intent: merge(selected[0].p, patch) });
    } else {
      const body: Record<string, PortIntent> = {};
      selected.forEach((pr) => {
        // PoE cannot be invented on a port that does not have one: the selection
        // can mix copper PoE and SFP, and force poe=true on an SFP
        // would write an intention that the material can never satisfy.
        const eff = patch.poe && !pr.poeCap ? { ...patch, poe: false } : patch;
        body[pr.name] = merge(pr.p, eff);
      });
      setPorts.mutate(body);
    }
    if (msg) showToast(msg);
  };
  const mutatePort = (pr: PortRef, patch: Partial<PortIntent>, msg?: string) => {
    const next = { ...pr.p, ...patch };
    if (patch.stp) next.stp = { ...pr.p.stp, ...patch.stp };
    setPort.mutate({ port: pr.name, intent: next });
    if (msg) showToast(msg);
  };

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
      <main>
        <div className="row" style={{ justifyContent: "space-between" }}>
          <ActionButton kind="close" className="ghost" onClick={() => navigate(sitePath(siteSlug, "equipment"))}>
            {t("switchDetailPage.backToDevices")}
          </ActionButton>
          <span className="row" style={{ gap: 8 }}>
            <span className="note">{syncLabel(dev)}</span>
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
        {dev.mgmt?.checked && !dev.mgmt.conforms && (
          // Outside the site addressing plan: meshcraft still watches the
          // device, but administration is not on the planned path yet.
          // Same visual family as config drift — an actionable gap, not a scare.
          <div className="drift-banner mgmt-warn-banner">
            <div className="drift-text">
              <div className="drift-title">{t("switchDetailPage.offMgmtPlan")}</div>
              <div className="note">
                {dev.mgmt.reason ??
                  t("switchDetailPage.defaultMgmtUnreachable")}
              </div>
              {dev.mgmt.path_state && dev.mgmt.path_state !== "ready" && (
                <div className="note" style={{ marginTop: 6, color: "var(--warn)" }}>
                  {dev.mgmt.path_reason}
                </div>
              )}
            </div>
            <div className="row" style={{ gap: 8 }}>
              {role === "ing" && (
                <ConformMgmtButton device={dev} onToast={showToast} />
              )}
              <ActionButton
                kind="link"
                className="ghost"
                title={t("switchDetailPage.openMgmtPlanTitle")}
                onClick={() => navigate(sitePath(siteSlug, "config", "management"))}
              >
                {t("switchDetailPage.viewPlan")}
              </ActionButton>
            </div>
          </div>
        )}
        {/* Equipment no one monitors anymore: the indicator vanished, and without
            this banner you would read controller logs to learn why. Placed at the
            very top — the only one of these banners that says meshcraft no longer
            knows anything about the device. */}
        {dev.watch_excluded && (
          <div className="drift-banner">
            <div className="drift-text">
              <div className="drift-title">{t("switchDetailPage.noLongerWatchedTitle")}</div>
              <div className="note">{dev.watch_excluded}</div>
              <div className="note" style={{ marginTop: 6 }}>
                {t("switchDetailPage.watchExcludedNote")}
              </div>
            </div>
          </div>
        )}
        {/* Just after: attachment became non-covering. Same family as the banner
            above — explain why an indicator vanished — but one notch less severe,
            because the device is still pinged and its status remains true. That
            is what makes it treacherous: nothing on screen would betray lost SNMP
            collection without this banner.

            No server-side equivalent, and there cannot be one: the controller
            refuses to WRITE a non-covering attachment; it cannot prevent coverage
            from shrinking afterward. */}
        {collectorStranded && (
          <div className="drift-banner">
            <div className="drift-text">
              <div className="drift-title">
                {t("switchDetailPage.noSnmpCollectorTitle")}
              </div>
              <div className="note">
                {t("switchDetailPage.collectorStrandedNote1", { name: dev.proxy_name || t("switchDetailPage.unknownProxy") })}
              </div>
              <div className="note" style={{ marginTop: 6 }}>
                {t("switchDetailPage.collectorStrandedNote2")}
              </div>
            </div>
          </div>
        )}
        {/* FIRST: failure is the only banner that signals nothing is in place. It
            must come before nuances. */}
        {dev.last_apply?.error && (
          <FailedApplyBanner
            error={dev.last_apply.error}
            busy={applyCfg.isPending || importCfg.isPending}
            onRetry={() => applyCfg.mutate(false)}
            onImport={() => importCfg.mutate()}
          />
        )}
        {(dev.last_apply?.skipped ?? []).length > 0 && (
          <SkippedBanner
            skipped={dev.last_apply?.skipped ?? []}
            busy={applyCfg.isPending || importCfg.isPending}
            onForce={() => applyCfg.mutate(true)}
            onCancel={() => importCfg.mutate()}
          />
        )}
        {/* After spared-port strip: this one calls for no decision, so it must not
            come before one that does. */}
        <UnsupportedBanner notes={dev.last_apply?.unsupported ?? []} />
        {/* LAST informative banner: describes neither failure nor refusal nor a
            platform limit — it describes what the device ACTUALLY does with
            traffic, re-read after apply. The controller produced these sentences
            from the start and nothing displayed them: a VLAN configuration can be
            applied, verified by re-read, counted conformant, and inert. */}
        <ConnectorNotesBanner notes={dev.last_apply?.notes ?? []} />

        {/* « In progress » but blocked: apply ran and did not finish everything.
            Reserved for `pending` — true drift has its own banner; showing two for
            one problem would blur both. */}
        {dev.sync === "pending" && dev.apply_stalled && (
          <IncompleteApplyBanner
            changes={dev.drift ?? []}
            busy={applyCfg.isPending || importCfg.isPending}
            onRetry={() => applyCfg.mutate(false)}
            onImport={() => importCfg.mutate()}
          />
        )}
        {dev.sync === "drift" && (
          <DriftBanner
            changes={dev.drift ?? []}
            busy={importCfg.isPending || applyCfg.isPending}
            onImport={() => importCfg.mutate()}
            onForce={() => applyCfg.mutate(false)}
          />
        )}
        <div className="row" style={{ alignItems: "flex-end", marginTop: 8 }}>
          <div style={{ minWidth: 0 }}>
            <div className="row" style={{ gap: 10 }}>
              <StatusDot dev={dev} />
              {role === "ing" ? (
                <QuickTip text={t("switchDetailPage.hostnameTitle")} className="quicktip-inline">
                  <input
                    key={dev.id + ":name"}
                    className="title-input"
                    defaultValue={dev.name}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") (e.target as HTMLInputElement).blur();
                    }}
                    onBlur={(e) => {
                      const v = e.target.value.trim();
                      if (v && v !== dev.name) saveDevice.mutate({ name: v });
                      else e.target.value = dev.name;
                    }}
                  />
                </QuickTip>
              ) : (
                <h1 style={{ margin: 0 }}>{dev.name}</h1>
              )}
              <ProtocolPills dev={dev} />
            </div>
            <div className="note" style={{ marginTop: 4 }}>
              {[dev.model, dev.location, dev.mgmt_ip, dev.extra?.os, dev.extra?.uptime && t("switchDetailPage.inServicePrefix") + dev.extra.uptime]
                .filter(Boolean)
                .join(" · ")}
            </div>
          </div>
          <div className="row" style={{ marginLeft: "auto", gap: 26, flex: "none" }}>
            <Metric k={t("switchDetailPage.switchThroughput")} v={off ? "—" : "↓ " + fmtMbps(rates.rx) + "  ↑ " + fmtMbps(rates.tx)} />
            <Metric k={t("switchDetailPage.activePorts")} v={rates.up + " / " + rates.total} />
            {dev.poe_budget_w > 0 && (
              <Metric
                k={t("switchDetailPage.poeBudget")}
                v={Math.round(rates.poe) + " / " + dev.poe_budget_w + " W"}
                color={poePct >= 85 ? "var(--warn)" : undefined}
              />
            )}
          </div>
        </div>
        <hr className="hr" style={{ margin: "12px 0 16px" }} />

        {off && (
          <div className="alert-banner">
            <span className="dot" style={{ background: "var(--danger)" }} />
            {t("switchDetailPage.unreachable")}{dev.status_note ? " — " + dev.status_note : t("switchDetailPage.lastKnownState")}
          </div>
        )}

        {facade.map((g) => (
          <div key={g.label} style={{ marginBottom: 18 }}>
            <div className="kicker" style={{ marginBottom: 6 }}>{g.label}</div>
            <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
              {g.rows.map((row, ri) => (
                <div key={ri} style={{ display: "flex", gap: 4, flexWrap: "wrap" }}>
                  {row.map((pr) => (
                    <PortSquare
                      key={pr.name}
                      name={pr.name}
                      displayName={pr.display}
                      port={pr.p}
                      vm={vm}
                      size={PORT_SIZE}
                      off={off}
                      hoverVlan={hoverVlan}
                      selected={selSet.has(pr.name)}
                      drift={driftPorts.get(pr.name)}
                      onClick={(e) => clickPort(pr.name, e)}
                    />
                  ))}
                </div>
              ))}
            </div>
          </div>
        ))}

        <div className="port-legend">
          {/* Selection shortcuts live HERE, against the facade they control, with
              help behind: read once. */}
          <span>
            {t("switchDetailPage.selection")}
            <HelpTip label={t("switchDetailPage.selectionHelpLabel")}>
              <p>
                {t("switchDetailPage.selectionHelp1")}
              </p>
              <ul>
                <li>{t("switchDetailPage.selectionHelpShift")}</li>
                <li>{t("switchDetailPage.selectionHelpCtrl")}</li>
                <li>{t("switchDetailPage.selectionHelpShiftCtrl")}</li>
              </ul>
              <p>
                {t("switchDetailPage.selectionHelpMulti")}
              </p>
            </HelpTip>
          </span>
          <span><i className="sw" style={{ background: "var(--accent)" }} /> {t("switchDetailPage.legendAccessActive")}</span>
          <span><i className="sw" style={{ background: "repeating-linear-gradient(135deg,var(--accent) 0 3px,var(--ok) 3px 6px)" }} /> {t("switchDetailPage.legendTrunk")}</span>
          <span><i className="sw" style={{ border: "1px solid var(--accent)", background: "transparent" }} /> {t("switchDetailPage.legendConfiguredDown")}</span>
          <span><i className="sw" style={{ background: "#64748b", border: "1px solid #94a3b8" }} /> {t("switchDetailPage.legendActiveUnmanaged")}</span>
          <span><i className="sw" style={{ background: "var(--bg-hover)", border: "1px solid var(--border)" }} /> {t("switchDetailPage.legendFree")}</span>
          <span><i className="sw poe" /> {t("switchDetailPage.legendPoeActive")}</span>
        </div>

        {/* TELEMETRY under the facade: what port boxes cannot carry. Here, not in
            the right panel, because these values describe the DEVICE not the
            selected port — putting them on the right would hide them as soon as a
            socket is clicked. */}
        <TelemetryPanel
          dev={dev}
          tel={tele}
          minutes={teleMinutes}
          onMinutes={setTeleMinutes}
          loading={teleFetching}
        />
      </main>

      <aside className="side-panel">
        {selected.length > 1 ? (
          <MultiPortPanel
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
            onClear={() => { setSelPorts([]); setSelProf(null); }}
          />
        ) : !sel ? (
          <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
            <SidePanelGroup title={t("switchDetailPage.switchSummary")} defaultOpen>
              <div className="kv" style={{ marginTop: 2 }}>
                <span className="k">{t("switchDetailPage.stateLabel")}</span>
                <span style={{ display: "flex", flexWrap: "wrap", gap: 8, alignItems: "center" }}>
                  <ProtocolPills dev={dev} />
                  <span style={{ color: statusColor(dev.status), fontWeight: 700 }}>
                    {reachability(dev).label}
                    {reachability(dev).detail ? (
                      <span style={{ fontWeight: 400, color: "var(--text-dim)" }}>
                        {" — " + reachability(dev).detail}
                      </span>
                    ) : null}
                  </span>
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
                {/* Domain only appears when there is something to choose: on a
                    single-network instance the question does not arise and the row
                    would only clutter. */}
                {role === "ing" && (domains ?? []).length > 1 && (
                  <>
                    <span className="k" style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
                      {t("switchDetailPage.networkLabel")}
                      <HelpTip label={t("switchDetailPage.networkHelpLabel")}>
                        <p>{t("switchDetailPage.networkHelp1")}</p>
                        <p>{t("switchDetailPage.networkHelp2")}</p>
                      </HelpTip>
                    </span>
                    <QuickTip text={t("switchDetailPage.networkTitle")} className="quicktip-inline">
                      <select
                        value={dev.address_domain_id ?? ""}
                        disabled={setDomain.isPending}
                        style={{ minHeight: 28, fontSize: 12 }}
                        onChange={(e) => setDomain.mutate(e.target.value)}
                      >
                        <option value="">{t("switchDetailPage.undeclaredNetwork")}</option>
                        {(domains ?? []).map((d) => (
                          <option key={d.id} value={d.id}>
                            {d.name}
                          </option>
                        ))}
                      </select>
                    </QuickTip>
                  </>
                )}
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
                {/* Collector, just after address: both say the same thing from two
                    angles — how we talk to this device, and who talks to it.

                    Row appears when there is something to DECIDE: at least two
                    covering proxies, or an attachment already declared. With one
                    covering proxy and no attachment, coverage already decides and
                    the row would teach nothing. The second condition is not a
                    convenience: without it, an attachment that became non-covering
                    would be flagged by the banner and impossible to fix on this page. */}
                {(covering.length > 1 || dev.proxy_id) && (
                  <>
                    <span className="k" style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
                      {t("switchDetailPage.collectorLabel")}
                      <HelpTip label={t("switchDetailPage.collectorHelpLabel")}>
                        <p>{t("switchDetailPage.collectorHelp1")}</p>
                        <p>{t("switchDetailPage.collectorHelp2")}</p>
                      </HelpTip>
                    </span>
                    {role !== "ing" ? (
                      // In read-only mode, the collector regime is spelled
                      // out in full. A "—" would suggest missing data
                      // where this is in fact normal operation.
                      <span>
                        {dev.proxy_name || t("switchDetailPage.noCollectorCoverage")}
                        {collectorStranded ? t("switchDetailPage.noLongerCoversSite") : ""}
                      </span>
                    ) : (
                      <QuickTip text={t("switchDetailPage.collectorSelectTitle")} className="quicktip-inline">
                        <select
                          value={dev.proxy_id ?? ""}
                          disabled={setCollector.isPending}
                          style={{ minHeight: 28, fontSize: 12 }}
                          onChange={(e) => setCollector.mutate(e.target.value)}
                        >
                        {/* Detachment listed first, and named: this is a fleet-wide regime,
                            not a checkbox for "none". */}
                        <option value="">{t("switchDetailPage.noCollectorOption")}</option>
                        {covering.map((p) => (
                          <option key={p.id} value={p.id}>
                            {p.name}
                            {p.global ? t("switchDetailPage.instanceProxySuffix") : ""}
                            {p.revoked ? t("switchDetailPage.revokedSuffix") : ""}
                          </option>
                        ))}
                        {/* Non-covering attachment is NOT in the list: without this entry
                            the select would show the first option and imply the device
                            is detached — a screen that would lie about the very state
                            it exists to fix. */}
                        {collectorStranded && (
                          <option value={dev.proxy_id}>
                            {t("switchDetailPage.unknownProxyNoCovers", { name: dev.proxy_name || t("switchDetailPage.unknownProxy") })}
                          </option>
                        )}
                      </select>
                      </QuickTip>
                    )}
                  </>
                )}
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
            dev={dev}
            pr={sel}
            role={role}
            off={off}
            vlans={vlans ?? []}
            profiles={profiles ?? []}
            traffic={portTraffic(tele, sel.name)}
            stepSeconds={tele?.step_seconds ?? 0}
            selProf={selProf}
            onSelProf={setSelProf}
            onApply={applyProfile}
            onPatch={(patch, msg) => mutatePort(sel, patch, msg)}
          />
        )}
        <ErrorMsg error={setPort.error} />
        <ErrorMsg error={setPorts.error} />
      </aside>
      {toast}
    </div>
  );
}

function Metric({ k, v, color }: { k: string; v: string; color?: string }) {
  return (
    <div>
      <div className="kicker" style={{ marginBottom: 3 }}>{k}</div>
      <div style={{ fontSize: 14, fontWeight: 700, whiteSpace: "pre", color: color ?? "var(--text)" }}>
        {v}
      </div>
    </div>
  );
}

/**
 * Collapsible block in the device side panel. Tall sections (management
 * addressing, VLAN plan) start folded so the default view stays a summary.
 */
function SidePanelGroup({
  title,
  meta,
  defaultOpen,
  children,
}: {
  title: string;
  meta?: ReactNode;
  defaultOpen: boolean;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <details
      className="side-panel-group"
      open={open}
      onToggle={(e) => setOpen(e.currentTarget.open)}
    >
      <summary className="side-panel-group-sum">
        <span className="kicker">{title}</span>
        {meta != null && meta !== "" && (
          <span className="note side-panel-group-meta">{meta}</span>
        )}
      </summary>
      <div className="side-panel-group-body">{children}</div>
    </details>
  );
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

/**
 * Normalize the same way the controller does (api.normalizeTag): lowercase,
 * spaces compacted. Duplicating the rule here isn't distrust of the
 * server — it's what lets us say "already exists" BEFORE the round trip,
 * and show the label in the toast exactly as it will actually be stored.
 */
/**
 * Equipment labels.
 * See docs/01-Architecture/Identifier labels and scope.
 *
 * A label is not harmless: a site administration credential can target
 * it, and the equipment then falls under a different EXPECTED password.
 * Hence the help icon — adding a label writes nothing to the equipment
 * itself, and the operator must be able to verify that without leaving
 * the form.
 *
 * The panel lives next to the equipment identity, not the ports: a label
 * qualifies the machine, never a single port.
 */
function DeviceTags({
  deviceId,
  siteId,
  canEdit,
  onToast,
  embedded,
}: {
  deviceId: string;
  siteId: string;
  canEdit: boolean;
  onToast: (msg: string) => void;
  /** Title lives on the side-panel group when true. */
  embedded?: boolean;
}) {
  const { t } = useTranslation();

  const qc = useQueryClient();

  const { data, error } = useQuery({
    queryKey: ["device-tags", deviceId],
    queryFn: () => get<{ tags: DeviceTag[] }>(`/api/devices/${deviceId}/tags`),
  });
  // Labels already in use on the site. Suggesting what already exists is
  // the only thing that keeps "regie" from coexisting with "régie": two
  // separate sets where the operator believed they were designating just
  // one, and a tag that ends up covering only half the intended fleet.
  const { data: siteTags } = useQuery({
    queryKey: ["site-tags", siteId],
    queryFn: () => get<{ tags: string[] }>(`/api/sites/${siteId}/tags`),
    enabled: canEdit,
  });

  // Placing the first occurrence of a label, or removing the last, changes
  // also the site list — which the Labels page and this entry reread.
  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["device-tags", deviceId] });
    qc.invalidateQueries({ queryKey: ["site-tags", siteId] });
  };

  const add = useMutation({
    // The tag travels in the PATH: it must be encoded — a raw "/" or
    // accent would break the route there.
    mutationFn: (tag: string) =>
      put<{
        tag: string;
        switch_profile_apply?: {
          ok?: boolean;
          profile_name?: string;
          applied?: number;
          issues?: string[];
          error?: string;
        };
      }>(`/api/devices/${deviceId}/tags/${encodeURIComponent(tag)}`),
    onSuccess: (res, tag) => {
      invalidate();
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      const apply = res?.switch_profile_apply;
      if (apply?.ok) {
        onToast(
          t("switchDetailPage.tagAppliedProfileToast", {
            tag: res?.tag ?? tag,
            profile: apply.profile_name ?? "",
            count: apply.applied ?? 0,
          }),
        );
      } else if (apply && apply.ok === false) {
        onToast(
          t("switchDetailPage.tagAppliedProfileBlockedToast", {
            tag: res?.tag ?? tag,
            profile: apply.profile_name ?? "",
            reason: apply.issues?.[0] ?? apply.error ?? "",
          }),
        );
      } else {
        onToast(t("switchDetailPage.tagAppliedToast", { tag: res?.tag ?? tag }));
      }
    },
  });
  const remove = useMutation({
    mutationFn: (t: DeviceTag) =>
      del(`/api/devices/${deviceId}/tags/${encodeURIComponent(t.tag)}`),
    onSuccess: (_res, removed) => {
      invalidate();
      // The toast repeats the consequence at the exact moment it happens:
      // for an inferred tag, the removal only lasts until the next
      // reassessment, and now is the moment to find out.
      onToast(
        isRuleTag(removed)
          ? t("switchDetailPage.tagRemovedRuleToast", { tag: removed.tag })
          : t("switchDetailPage.tagRemovedToast", { tag: removed.tag }),
      );
    },
  });

  const tags = data?.tags ?? [];
  const hasRule = tags.some(isRuleTag);
  // Two populations of labels coexist on the same equipment, and they are
  // not treated the same way (see the closing note of §3.1 of the labels
  // page): operational tags, arbitrary and removable, and TECHNICAL tags,
  // placed by meshcraft, which determine an identifier. The second family
  // has two mechanisms — the SNMPv3 identity and the password tied to the
  // platform's password policy — hence a help paragraph that says what to
  // look for, and where, shown only when the equipment carries one.
  const technical = tags.filter(isTechnicalTag);

  return (
    <div>
      {!embedded && (
        <div className="kicker">
          {t("switchDetailPage.tagsTitle")}
          <HelpTip label={t("switchDetailPage.tagsHelpLabel")}>
            <p>{t("switchDetailPage.tagsHelp1")}</p>
            {technical.length > 0 && (
              <p>{t("switchDetailPage.tagsHelpTechnical")}</p>
            )}
            {hasRule && (
              <p>{t("switchDetailPage.tagsHelpRule")}</p>
            )}
          </HelpTip>
        </div>
      )}
      <div style={{ marginTop: embedded ? 0 : 8 }}>
        <TagInput
          tags={tags}
          suggestions={siteTags?.tags ?? []}
          readOnly={!canEdit}
          busy={add.isPending || remove.isPending}
          onAdd={(t) => add.mutate(t)}
          onRemove={(t) => remove.mutate(t)}
        />
      </div>

      <ErrorMsg error={error ?? add.error ?? remove.error} />
    </div>
  );
}

function PortPanel({
  dev,
  pr,
  role,
  off,
  vlans,
  profiles,
  traffic,
  stepSeconds,
  selProf,
  onSelProf,
  onApply,
  onPatch,
}: {
  dev: Device;
  pr: PortRef;
  role: "ing" | "tech" | "viewer";
  off: boolean;
  vlans: Vlan[];
  profiles: Profile[];
  /** Both directions of traffic from THIS port, measured (see ../telemetry). */
  traffic: { rx: (number | null)[]; tx: (number | null)[] };
  /** The pitch of the window served, to name the points of the axis. */
  stepSeconds: number;
  selProf: string | null;
  onSelProf: (id: string | null) => void;
  onApply: () => void;
  onPatch: (patch: Partial<PortIntent>, msg?: string) => void;
}) {
  const { t } = useTranslation();

  const p = pr.p;
  const vm = vlanMap(vlans);
  const canApply = role !== "viewer" && !off;
  const ing = role === "ing";

  // THE HISTORY OF BOTH DIRECTIONS, MEASURED.
  //
  // It used to be MANUFACTURED: thirty points generated by a deterministic
  // jitter around the current value, drawing a plausible-looking curve for
  // a past that had never actually been measured. It now comes from the
  // time-series store (GET /api/devices/{id}/telemetry), i.e. real
  // equipment counters.
  //
  // An unreachable switch keeps its curve: that's what happened BEFORE it
  // went down, and that's exactly what we come here to look at. Only the
  // "now" value is forced to zero, because no traffic flows through
  // silent equipment.
  const rxHist = traffic.rx;
  const txHist = traffic.tx;
  const lastNum = (xs: (number | null)[]) => {
    var i = xs.length - 1;
    for (; i >= 0; i--) {
      if (xs[i] != null) return xs[i] as number;
    }
    return null;
  };
  const rxNow = off ? 0 : (lastNum(rxHist) ?? p.rx_mbps ?? 0);
  const txNow = off ? 0 : (lastNum(txHist) ?? p.tx_mbps ?? 0);
  const hasHist =
    rxHist.some((v) => v != null) || txHist.some((v) => v != null);

  const tagged = p.tagged ?? [];
  const toggleTrunkMember = (vid: number) => {
    let next: number[] =
      tagged === "all" ? vlans.map((v) => v.vlan_id) : (tagged as number[]).slice();
    const i = next.indexOf(vid);
    if (i > -1) next.splice(i, 1);
    else next.push(vid);
    next.sort((a, b) => a - b);
    onPatch({ tagged: next });
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
      <div>
        <div className="kicker">{dev.name} · {pr.name} · {pr.speed}</div>
        {ing && !off ? (
          <input
            className="port-label-input"
            value={p.label ?? ""}
            placeholder={t("switchDetailPage.portLabelPlaceholder")}
            onChange={(e) => onPatch({ label: e.target.value })}
          />
        ) : (
          <div style={{ fontSize: 16, fontWeight: 700, marginTop: 8 }}>
            {p.label || pr.name + t("switchDetailPage.portFreeSuffix")}
          </div>
        )}
      </div>

      <div className="kv" style={{ borderTop: "1px solid var(--border)", paddingTop: 12 }}>
        <span className="k">{t("switchDetailPage.linkLabel")}</span>
        <span style={{ color: off ? "var(--text-dim)" : p.link ? "var(--ok)" : "var(--text-dim)", fontWeight: 600 }}>
          {off ? t("switchDetailPage.switchUnreachableShort") : p.link ? p.link + t("switchDetailPage.linkFullDuplex") : t("switchDetailPage.noLinkDetected")}
        </span>
        <span className="k">PoE</span>
        <span>
          {!pr.poeCap
            ? t("switchDetailPage.poeNotAvailable")
            : (p.poe_w ?? 0) > 0
              ? String(p.poe_w).replace(".", ",") + t("switchDetailPage.poeActiveWatts")
              : p.poe
                ? t("switchDetailPage.poeEnabledWaiting")
                : t("switchDetailPage.poeOff")}
        </span>
        <span className="k">{t("switchDetailPage.trafficLabel")}</span>
        <span style={{ whiteSpace: "pre" }}>{"↓ " + fmtMbps(rxNow) + "  ↑ " + fmtMbps(txNow)}</span>
      </div>

      <div>
        <div className="kicker" style={{ marginBottom: 4 }}>
          {t("switchDetailPage.portTrafficTitle")}
        </div>
        {hasHist ? (
          <LineChart
            height={132}
            series={[
              { name: t("switchDetailPage.chartIncoming"), color: SERIES.in, points: rxHist },
              { name: t("switchDetailPage.chartOutgoing"), color: SERIES.out, points: txHist },
            ]}
            format={fmtMbps}
            xLabel={(i, n) => {
              if (i === n - 1) return t("switchDetailPage.chartNow");
              const back = (n - 1 - i) * stepSeconds;
              if (back < 90) return t("switchDetailPage.chartSecondsAgo", { n: Math.round(back) });
              if (back < 5400) return t("switchDetailPage.chartMinutesAgo", { n: Math.round(back / 60) });
              if (back < 48 * 3600) return t("switchDetailPage.chartHoursAgo", { n: Math.round(back / 3600) });
              return t("switchDetailPage.chartDaysAgo", { n: Math.round(back / 86400) });
            }}
          />
        ) : (
          <div className="note">
            {t("switchDetailPage.noTrafficNote")}
          </div>
        )}
      </div>

      {off && (
        <div className="note" style={{ border: "1px solid var(--border)", padding: "10px 12px", borderRadius: 8 }}>
          {t("switchDetailPage.unreachableConfigNote")}
        </div>
      )}

      {!off && canApply && (
        <div style={{ borderTop: "2px solid var(--border)", paddingTop: 12 }}>
          <div className="kicker" style={{ marginBottom: 8 }}>{t("switchDetailPage.portProfilesTitle")}</div>
          <div style={{ display: "flex", flexDirection: "column", gap: 5 }}>
            {profiles.map((prof) => {
              const spec = prof.spec as unknown as PortProfileSpec;
              const locked = !!spec.eng_only && !ing;
              const selp = selProf === prof.id;
              const swatchIds =
                spec.mode === "access"
                  ? [spec.pvid]
                  : spec.tagged === "all"
                    ? [spec.pvid]
                    : ((spec.tagged ?? []) as number[]).slice(0, 4);
              return (
                <button
                  key={prof.id}
                  disabled={locked}
                  className={"prof-row" + (selp ? " sel" : "")}
                  onClick={() => onSelProf(prof.id)}
                >
                  <span style={{ display: "flex", gap: 2, flex: "none" }}>
                    {swatchIds.map((id, i) => (
                      <span
                        key={i}
                        style={{
                          width: 9, height: 9, borderRadius: 2, display: "block",
                          background: id != null && vm.get(id) ? vm.get(id)!.color : "#888",
                        }}
                      />
                    ))}
                  </span>
                  <span style={{ fontSize: 12, fontWeight: 700 }}>{prof.name}</span>
                  <span className="note" style={{ marginLeft: "auto", whiteSpace: "nowrap", fontSize: 9, letterSpacing: ".05em" }}>
                    {(spec.mode === "access"
                      ? t("switchDetailPage.accessPrefix", { pvid: spec.pvid ?? "" })
                      : spec.tagged === "all" ? "TRUNK ▤▤▤" : "TRUNK") + (spec.poe ? " · POE" : "")}
                  </span>
                </button>
              );
            })}
            {profiles.length === 0 && <div className="note">{t("switchDetailPage.noProfilesDefined")}</div>}
          </div>
          <ActionButton kind="apply" style={{ width: "100%", marginTop: 10 }} disabled={!selProf} onClick={onApply}>
            {t("switchDetailPage.applyProfileBtn")}
          </ActionButton>
          {!ing && (
            <div className="note" style={{ fontSize: 10, marginTop: 6 }}>
              {t("switchDetailPage.engineerProfilesNote")}
            </div>
          )}
        </div>
      )}

      {!off && ing && (
        <div style={{ borderTop: "2px solid var(--border)", paddingTop: 12 }}>
          <div className="kicker" style={{ marginBottom: 8 }}>{t("switchDetailPage.manualConfigTitle")}</div>
          <div className="seg3">
            {(["access", "trunk", "libre"] as const).map((m) => (
              <button
                key={m}
                className={p.mode === m ? "on" : ""}
                onClick={() =>
                  onPatch(
                    m === "trunk"
                      ? { mode: m, tagged: tagged === "all" || (Array.isArray(tagged) && tagged.length) ? tagged : p.pvid != null ? [p.pvid] : [] }
                      : { mode: m },
                  )
                }
              >
                {m === "access" ? t("switchDetailPage.modeAccess") : m === "trunk" ? t("switchDetailPage.modeTrunk") : t("switchDetailPage.modeFree")}
              </button>
            ))}
          </div>

          {p.mode === "access" && (
            <div className="vlan-pick">
              {vlans.map((v) => (
                <button
                  key={v.id}
                  className={"vlan-row" + (p.pvid === v.vlan_id ? " sel" : "")}
                  onClick={() => onPatch({ mode: "access", pvid: v.vlan_id, tagged: [] })}
                >
                  <span className="vlan-chip" style={{ background: v.color, margin: 0 }} />
                  <span style={{ fontWeight: 600 }}>{v.name}</span>
                  <span className="note">{v.vlan_id}</span>
                </button>
              ))}
            </div>
          )}

          {p.mode === "trunk" && (
            <>
              <div className="kicker" style={{ margin: "10px 0 6px" }}>{t("switchDetailPage.taggedMembersTitle")}</div>
              <div className="vlan-pick">
                <button
                  className="vlan-row"
                  onClick={() =>
                    onPatch({ tagged: tagged === "all" ? (p.pvid != null ? [p.pvid] : []) : "all" })
                  }
                >
                  <span
                    className="vlan-chip"
                    style={{ background: tagged === "all" ? "var(--accent)" : "transparent", border: "1px solid var(--border)", margin: 0 }}
                  />
                  <span style={{ fontWeight: 600 }}>{t("switchDetailPage.fullTrunkAllVlans")}</span>
                </button>
                {vlans.map((v) => {
                  const member = tagged === "all" || (Array.isArray(tagged) && tagged.includes(v.vlan_id));
                  return (
                    <button key={v.id} className="vlan-row" onClick={() => toggleTrunkMember(v.vlan_id)}>
                      <span
                        className="vlan-chip"
                        style={{ background: member ? v.color : "transparent", border: `1px solid ${v.color}`, margin: 0 }}
                      />
                      <span style={{ fontWeight: 600, color: member ? "var(--text)" : "var(--text-dim)" }}>
                        {v.name}
                      </span>
                      <span className="note">{v.vlan_id}</span>
                    </button>
                  );
                })}
              </div>
              <div className="kicker" style={{ margin: "10px 0 5px" }}>{t("switchDetailPage.nativeVlanTitle")}</div>
              <select
                value={p.pvid ?? ""}
                onChange={(e) => onPatch({ pvid: e.target.value ? Number(e.target.value) : null })}
              >
                <option value="">—</option>
                {vlans.map((v) => (
                  <option key={v.id} value={v.vlan_id}>{v.vlan_id} — {v.name}</option>
                ))}
              </select>
            </>
          )}

          {pr.poeCap && p.mode !== "libre" && (
            <div className="row" style={{ marginTop: 12 }}>
              <span className="kicker">PoE</span>
              <div className="seg3" style={{ marginBottom: 0 }}>
                <button
                  className={p.poe ? "on" : ""}
                  onClick={() => onPatch({ poe: true }, t("switchDetailPage.poeEnabledToast", { name: pr.name }))}
                >
                  {t("switchDetailPage.poePoweredBtn")}
                </button>
                <button
                  className={!p.poe ? "on" : ""}
                  onClick={() => onPatch({ poe: false, poe_w: 0 }, t("switchDetailPage.poeDisabledToast", { name: pr.name }))}
                >
                  {t("switchDetailPage.poeCutBtn")}
                </button>
              </div>
            </div>
          )}

          {p.mode !== "libre" && (dev.stp_caps?.edge || dev.stp_caps?.port_enable || dev.stp_caps?.bpdu_guard) && (
            <div style={{ marginTop: 12 }}>
              {dev.stp_caps?.edge && (
                <div className="row" style={{ marginBottom: 8 }}>
                  <span className="kicker">{t("switchDetailPage.stpEdgeLabel")}</span>
                  <div className="seg3" style={{ marginBottom: 0 }}>
                    {(dev.stp_caps.edge_auto
                      ? (["auto", "yes", "no"] as const)
                      : (["yes", "no"] as const)
                    ).map((e) => (
                      <button
                        key={e}
                        className={String(p.stp?.edge ?? "") === e ? "on" : ""}
                        onClick={() =>
                          onPatch(
                            { stp: { edge: e } },
                            t("switchDetailPage.stpEdgeToast", { name: pr.name, edge: e }),
                          )
                        }
                      >
                        {e === "auto"
                          ? t("switchDetailPage.stpEdgeAuto")
                          : e === "yes"
                            ? t("switchDetailPage.stpEdgeYes")
                            : t("switchDetailPage.stpEdgeNo")}
                      </button>
                    ))}
                  </div>
                </div>
              )}
              {dev.stp_caps?.port_enable && (
                <div className="row" style={{ marginBottom: 8 }}>
                  <span className="kicker">{t("switchDetailPage.stpPortEnableLabel")}</span>
                  <div className="seg3" style={{ marginBottom: 0 }}>
                    <button
                      className={p.stp?.enabled !== false ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { enabled: true } },
                          t("switchDetailPage.stpPortOnToast", { name: pr.name }),
                        )
                      }
                    >
                      {t("switchDetailPage.stpPortOn")}
                    </button>
                    <button
                      className={p.stp?.enabled === false ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { enabled: false } },
                          t("switchDetailPage.stpPortOffToast", { name: pr.name }),
                        )
                      }
                    >
                      {t("switchDetailPage.stpPortOff")}
                    </button>
                  </div>
                </div>
              )}
              {dev.stp_caps?.bpdu_guard && (
                <div className="row">
                  <span className="kicker">{t("switchDetailPage.bpduGuardLabel")}</span>
                  <div className="seg3" style={{ marginBottom: 0 }}>
                    <button
                      className={p.stp?.bpdu_guard === true ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { bpdu_guard: true } },
                          t("switchDetailPage.bpduGuardOnToast", { name: pr.name }),
                        )
                      }
                    >
                      {t("switchDetailPage.bpduGuardOn")}
                    </button>
                    <button
                      className={p.stp?.bpdu_guard === false ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { bpdu_guard: false } },
                          t("switchDetailPage.bpduGuardOffToast", { name: pr.name }),
                        )
                      }
                    >
                      {t("switchDetailPage.bpduGuardOff")}
                    </button>
                  </div>
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

/**
 * Compacts a selection into ranges: "1-8, 12, 24-26". A panel that
 * announces "1, 2, 3, 4, 5, 6, 7, 8" forces you to count again to verify
 * that we actually selected what we thought we did.
 */
function rangeLabel(sel: PortRef[], all: PortRef[]): string {
  // A port not found in `all` is discarded rather than crashing the
  // render: an incomplete label is a display glitch, an exception here
  // would blank out the whole page.
  const idx = sel.map((p) => all.indexOf(p)).filter((i) => i >= 0).sort((a, b) => a - b);
  if (idx.length === 0) return sel.map((p) => p.name).join(", ");
  const out: string[] = [];
  let i = 0;
  while (i < idx.length) {
    let j = i;
    while (j + 1 < idx.length && idx[j + 1] === idx[j] + 1) j++;
    const a = all[idx[i]].name;
    const b = all[idx[j]].name;
    out.push(i === j ? a : j === i + 1 ? `${a}, ${b}` : `${a}–${b}`);
    i = j + 1;
  }
  return out.join(", ");
}

/** Common value for the entire selection, or `undefined` if they differ. */
function common<T>(sel: PortRef[], of: (p: PortRef) => T): T | undefined {
  const first = of(sel[0]);
  return sel.every((p) => of(p) === first) ? first : undefined;
}

/**
 * Multiple selection panel.
 *
 * Two principles hold this screen:
 *
 * 1. what DIFFERS between ports is shown as such ("mixed"), never
 *    reduced to the value of the first — otherwise the screen would lie
 *    about what we're about to overwrite;
 * 2. nothing is sent until you click: on twelve ports, a toggle applied
 *    on hover would be a permanent accident.
 */
function MultiPortPanel({
  dev,
  sel,
  all,
  role,
  off,
  vlans,
  profiles,
  selProf,
  onSelProf,
  onApply,
  onPatch,
  onClear,
}: {
  dev: Device;
  sel: PortRef[];
  /** The SAME list that `sel` came from: `expandPorts` reconstructs
   * new objects on each call, and an `indexOf` on a copy returns -1. */
  all: PortRef[];
  role: "ing" | "tech" | "viewer";
  off: boolean;
  vlans: Vlan[];
  profiles: Profile[];
  selProf: string | null;
  onSelProf: (id: string | null) => void;
  onApply: () => void;
  onPatch: (patch: Partial<PortIntent>, msg?: string) => void;
  onClear: () => void;
}) {
  const { t } = useTranslation();

  const vm = vlanMap(vlans);
  const ing = role === "ing";
  const canApply = role !== "viewer" && !off;
  const [label, setLabel] = useState("");

  const mode = common(sel, (p) => p.p.mode ?? "libre");
  const pvid = common(sel, (p) => p.p.pvid ?? null);
  const poe = common(sel, (p) => !!p.p.poe);
  const withPoe = sel.filter((p) => p.poeCap).length;
  const linked = sel.filter((p) => !!p.p.link).length;

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
      <div>
        <div className="kicker">{dev.name} · {t("switchDetailPage.selectionKicker")}</div>
        <div style={{ fontSize: 16, fontWeight: 700, marginTop: 8 }}>
          {sel.length} ports
        </div>
        <div className="note mono" style={{ marginTop: 2 }}>{rangeLabel(sel, all)}</div>
        <ActionButton kind="cancel" className="ghost" style={{ marginTop: 6 }} onClick={onClear}>
          {t("switchDetailPage.clearSelection")}
        </ActionButton>
      </div>

      <div className="kv" style={{ borderTop: "1px solid var(--border)", paddingTop: 12 }}>
        <span className="k">{t("switchDetailPage.modeLabel")}</span>
        <span style={{ fontWeight: 600 }}>
          {mode === undefined ? <i style={{ color: "var(--warn)" }}>{t("switchDetailPage.modeMixed")}</i>
            : mode === "access" ? t("switchDetailPage.modeAccessLabel") : mode === "trunk" ? t("switchDetailPage.modeTrunkLabel") : t("switchDetailPage.modeFreeLabel")}
        </span>
        <span className="k">{t("switchDetailPage.vlanLabel")}</span>
        <span>
          {pvid === undefined ? <i style={{ color: "var(--warn)" }}>{t("switchDetailPage.modeMixed")}</i>
            : pvid === null ? "—"
              : (vm.get(pvid)?.name ?? "VLAN " + pvid) + " (" + pvid + ")"}
        </span>
        <span className="k">{t("switchDetailPage.activeLinksLabel")}</span>
        <span>{off ? "—" : linked + " / " + sel.length}</span>
        <span className="k">PoE</span>
        <span>
          {withPoe === 0 ? t("switchDetailPage.noPoeInSelection")
            : (poe === undefined ? t("switchDetailPage.modeMixed") : poe ? t("switchDetailPage.powered") : t("switchDetailPage.cut")) +
              (withPoe < sel.length ? t("switchDetailPage.poeCapableNote", { capable: withPoe, total: sel.length }) : "")}
        </span>
      </div>

      {off && (
        <div className="note" style={{ border: "1px solid var(--border)", padding: "10px 12px", borderRadius: 8 }}>
          {t("switchDetailPage.unreachableMultiNote")}
        </div>
      )}

      {!off && canApply && (
        <div style={{ borderTop: "2px solid var(--border)", paddingTop: 12 }}>
          <div className="kicker" style={{ marginBottom: 8 }}>{t("switchDetailPage.portProfilesTitle")}</div>
          <div style={{ display: "flex", flexDirection: "column", gap: 5 }}>
            {profiles.map((prof) => {
              const spec = prof.spec as unknown as PortProfileSpec;
              const locked = !!spec.eng_only && !ing;
              return (
                <button
                  key={prof.id}
                  disabled={locked}
                  className={"prof-row" + (selProf === prof.id ? " sel" : "")}
                  onClick={() => onSelProf(prof.id)}
                >
                  <span style={{ fontSize: 12, fontWeight: 700 }}>{prof.name}</span>
                  <span className="note" style={{ marginLeft: "auto", whiteSpace: "nowrap", fontSize: 9, letterSpacing: ".05em" }}>
                    {(spec.mode === "access"
                      ? t("switchDetailPage.accessPrefix", { pvid: spec.pvid ?? "" })
                      : "TRUNK") + (spec.poe ? " · POE" : "")}
                  </span>
                </button>
              );
            })}
            {profiles.length === 0 && <div className="note">{t("switchDetailPage.noProfilesDefined")}</div>}
          </div>
          <ActionButton kind="apply" style={{ width: "100%", marginTop: 10 }} disabled={!selProf} onClick={onApply}>
            {t("switchDetailPage.applyToNPorts", { count: sel.length })}
          </ActionButton>
        </div>
      )}

      {!off && ing && (
        <div style={{ borderTop: "2px solid var(--border)", paddingTop: 12 }}>
          <div className="kicker" style={{ marginBottom: 8 }}>{t("switchDetailPage.manualConfigTitle")}</div>
          <div className="seg3">
            {(["access", "trunk", "libre"] as const).map((m) => (
              <button
                key={m}
                className={mode === m ? "on" : ""}
                onClick={() =>
                  onPatch(
                    m === "libre" ? { mode: m } : { mode: m },
                    t("switchDetailPage.portsToModeToast", { count: sel.length, mode: m.toUpperCase() }),
                  )
                }
              >
                {m === "access" ? t("switchDetailPage.modeAccess") : m === "trunk" ? t("switchDetailPage.modeTrunk") : t("switchDetailPage.modeFree")}
              </button>
            ))}
          </div>

          {(mode === "access" || mode === undefined) && (
            <>
              <div className="kicker" style={{ margin: "10px 0 6px" }}>{t("switchDetailPage.accessVlanTitle")}</div>
              <div className="vlan-pick">
                {vlans.map((v) => (
                  <button
                    key={v.id}
                    className={"vlan-row" + (pvid === v.vlan_id ? " sel" : "")}
                    onClick={() =>
                      onPatch(
                        { mode: "access", pvid: v.vlan_id, tagged: [] },
                        t("switchDetailPage.portsToAccessToast", { count: sel.length, name: v.name }),
                      )
                    }
                  >
                    <span className="vlan-chip" style={{ background: v.color, margin: 0 }} />
                    <span style={{ fontWeight: 600 }}>{v.name}</span>
                    <span className="note">{v.vlan_id}</span>
                  </button>
                ))}
              </div>
            </>
          )}

          {mode === "trunk" && (
            <>
              <div className="kicker" style={{ margin: "10px 0 6px" }}>
                {t("switchDetailPage.taggedMembersTitle")}
                <HelpTip label={t("switchDetailPage.taggedMembersHelpLabel")}>
                  <p>
                    {t("switchDetailPage.taggedMembersHelp")}
                  </p>
                </HelpTip>
              </div>
              <div className="vlan-pick">
                <button
                  className="vlan-row"
                  onClick={() => onPatch({ tagged: "all" }, t("switchDetailPage.fullTrunkSelectionToast", { count: sel.length }))}
                >
                  <span className="vlan-chip" style={{ background: "var(--accent)", margin: 0 }} />
                  <span style={{ fontWeight: 600 }}>{t("switchDetailPage.fullTrunkAllVlans")}</span>
                </button>
              </div>
            </>
          )}

          {withPoe > 0 && (
            <div className="row" style={{ marginTop: 12 }}>
              <span className="kicker">PoE</span>
              <div className="seg3" style={{ marginBottom: 0 }}>
                <button
                  className={poe === true ? "on" : ""}
                  onClick={() => onPatch({ poe: true }, t("switchDetailPage.poeEnabledMultiToast", { count: withPoe }))}
                >
                  {t("switchDetailPage.poePoweredBtn")}
                </button>
                <button
                  className={poe === false ? "on" : ""}
                  onClick={() => onPatch({ poe: false, poe_w: 0 }, t("switchDetailPage.poeDisabledMultiToast", { count: withPoe }))}
                >
                  {t("switchDetailPage.poeCutBtn")}
                </button>
              </div>
            </div>
          )}

          {mode !== "libre" && (dev.stp_caps?.edge || dev.stp_caps?.port_enable || dev.stp_caps?.bpdu_guard) && (
            <div style={{ marginTop: 12 }}>
              {dev.stp_caps?.edge && (
                <div className="row" style={{ marginBottom: 8 }}>
                  <span className="kicker">{t("switchDetailPage.stpEdgeLabel")}</span>
                  <div className="seg3" style={{ marginBottom: 0 }}>
                    {(dev.stp_caps.edge_auto
                      ? (["auto", "yes", "no"] as const)
                      : (["yes", "no"] as const)
                    ).map((e) => (
                      <button
                        key={e}
                        className={common(sel, (p) => String(p.p.stp?.edge ?? "")) === e ? "on" : ""}
                        onClick={() =>
                          onPatch(
                            { stp: { edge: e } },
                            t("switchDetailPage.stpEdgeToast", { name: `${sel.length} ports`, edge: e }),
                          )
                        }
                      >
                        {e === "auto"
                          ? t("switchDetailPage.stpEdgeAuto")
                          : e === "yes"
                            ? t("switchDetailPage.stpEdgeYes")
                            : t("switchDetailPage.stpEdgeNo")}
                      </button>
                    ))}
                  </div>
                </div>
              )}
              {dev.stp_caps?.port_enable && (
                <div className="row" style={{ marginBottom: 8 }}>
                  <span className="kicker">{t("switchDetailPage.stpPortEnableLabel")}</span>
                  <div className="seg3" style={{ marginBottom: 0 }}>
                    <button
                      className={common(sel, (p) => p.p.stp?.enabled !== false) === true ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { enabled: true } },
                          t("switchDetailPage.stpPortOnToast", { name: `${sel.length} ports` }),
                        )
                      }
                    >
                      {t("switchDetailPage.stpPortOn")}
                    </button>
                    <button
                      className={common(sel, (p) => p.p.stp?.enabled === false) === true ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { enabled: false } },
                          t("switchDetailPage.stpPortOffToast", { name: `${sel.length} ports` }),
                        )
                      }
                    >
                      {t("switchDetailPage.stpPortOff")}
                    </button>
                  </div>
                </div>
              )}
              {dev.stp_caps?.bpdu_guard && (
                <div className="row">
                  <span className="kicker">{t("switchDetailPage.bpduGuardLabel")}</span>
                  <div className="seg3" style={{ marginBottom: 0 }}>
                    <button
                      className={common(sel, (p) => p.p.stp?.bpdu_guard === true) === true ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { bpdu_guard: true } },
                          t("switchDetailPage.bpduGuardOnToast", { name: `${sel.length} ports` }),
                        )
                      }
                    >
                      {t("switchDetailPage.bpduGuardOn")}
                    </button>
                    <button
                      className={common(sel, (p) => p.p.stp?.bpdu_guard === false) === true ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { bpdu_guard: false } },
                          t("switchDetailPage.bpduGuardOffToast", { name: `${sel.length} ports` }),
                        )
                      }
                    >
                      {t("switchDetailPage.bpduGuardOff")}
                    </button>
                  </div>
                </div>
              )}
            </div>
          )}

          <div className="kicker" style={{ margin: "12px 0 5px" }}>{t("switchDetailPage.commonLabelTitle")}</div>
          <div className="row" style={{ gap: 6 }}>
            <input
              value={label}
              placeholder={t("switchDetailPage.commonLabelPlaceholder")}
              onChange={(e) => setLabel(e.target.value)}
              style={{ minHeight: 28, fontSize: 12 }}
            />
            <ActionButton
              kind="apply"
              className="secondary"
              disabled={!label.trim()}
              onClick={() => {
                onPatch({ label: label.trim() }, t("switchDetailPage.portsTaggedToast", { count: sel.length }));
                setLabel("");
              }}
            >
              Apply
            </ActionButton>
          </div>
        </div>
      )}
    </div>
  );
}














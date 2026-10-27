import { useQuery } from "@tanstack/react-query";
import { Fragment, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  NavLink,
  Outlet,
  useLocation,
  useNavigate,
  useParams,
} from "react-router-dom";
import { get, type Site } from "./api";
import { useAuth } from "./auth";
import { QuickTip } from "./components";
import {
  destinationOnSwitch,
  modeForPath,
  persistMode,
  storedMode,
  tabsForMode,
  type NavMode,
} from "./navMode";
import { LAST_SITE_KEY, sitePath } from "./paths";
import AccountMenu from "./ui/AccountMenu";
import SiteSelect from "./ui/SiteSelect";
import ClockSkewBanner from "./ui/ClockSkewBanner";
import { useBreakpoint } from "./ui/breakpoint";
import { useDismissable } from "./ui/dismiss";
import ModeSwitch from "./ui/ModeSwitch";
import { NAV_ICONS } from "./ui/NavIcons";
import { useSwipeNav } from "./ui/swipe";
import { stepIndex, type SwipeAxis, type SwipeDir } from "./ui/swipeDecide";

const CONFIG_SIDEBAR_KEY = "meshcraft.configSidebarCollapsed";

/**
 * A destination, in the order the menu puts it. The swipe needs the sidebar
 * as DATA, not as JSX — "the next page down" cannot be read out of markup —
 * and the same list then renders it, so the two can never disagree.
 *
 * `away: true` marks an entry that LEAVES the current section (the global
 * credentials, reached from Add). It keeps its ↗ and it is excluded from the
 * swipe order: a gesture must not drop you out of an area by accident.
 */
type Dest = { to: string; label: string; away?: boolean };
type SideGroup = { title: string; items: Dest[] };

/**
 * Footer of the admin menu: what is actually running. The plain product
 * version, with the build timestamp below it — that's the one that changes
 * with every image and answers "has my fix been deployed?".
 */
function VersionFooter() {
  const { data } = useQuery({
    queryKey: ["version"],
    queryFn: () => get<{ version: string; build: string }>("/api/version"),
    staleTime: Infinity,
  });
  if (!data) return null;
  return (
    <QuickTip text={`build ${data.build}`} className="quicktip-block">
      <div className="sidebar-footer">
        <span>meshcraft {data.version}</span>
        <span className="mono">{data.build}</span>
      </div>
    </QuickTip>
  );
}

function resolveSlug(sites: Site[] | undefined, routeSlug: string | undefined): string {
  if (!sites || sites.length === 0) return "";
  if (routeSlug) {
    const hit = sites.find((s) => s.slug === routeSlug || s.id === routeSlug);
    if (hit) return hit.slug;
  }
  const stored = localStorage.getItem(LAST_SITE_KEY) ?? "";
  if (stored && sites.some((s) => s.slug === stored || s.id === stored)) {
    const hit = sites.find((s) => s.slug === stored || s.id === stored);
    return hit?.slug ?? sites[0].slug;
  }
  return sites[0].slug;
}

/**
 * App shell: main tabs at the top and a sidebar reserved for Configuration
 * and for Add.
 *
 * THE TABS DEPEND ON THE NAVIGATION MODE — ADR-0013. Switching shows
 * Dashboard · Equipment · Ports · Topology · Clients; routing shows
 * Dashboard · Routing · Firewall · DNS · DHCP. The mode is carried by the
 * brand mark (ui/ModeSwitch.tsx), it filters the navigation and nothing
 * else, and the URL always wins over the stored preference.
 *
 * UNDER 720px THE SHELL INVERTS. The tabs move to a glass bar at the
 * BOTTOM, where a thumb is, and the sidebar becomes a drawer. The top bar
 * had seven children in one flex row; on a phone it wrapped onto three
 * rows and still clipped the last tab, spending 280px of a 852px screen
 * before the first pixel of content — on exactly the screens that get
 * opened standing in front of a rack. See UI-GUIDE §9.4.
 */
export default function Layout() {
  const { t } = useTranslation();
  const { user, logout } = useAuth();
  const { siteSlug: routeSlug } = useParams();
  const navigate = useNavigate();
  const location = useLocation();
  const bp = useBreakpoint();
  const narrow = bp === "narrow";

  const { data: sites } = useQuery({
    queryKey: ["sites"],
    queryFn: () => get<Site[]>("/api/sites"),
  });

  const siteSlug = resolveSlug(sites, routeSlug);
  useEffect(() => {
    if (routeSlug && sites) {
      const hit = sites.find((s) => s.slug === routeSlug || s.id === routeSlug);
      if (hit) localStorage.setItem(LAST_SITE_KEY, hit.slug);
    }
  }, [routeSlug, sites]);

  const [configCollapsed, setConfigCollapsed] = useState(() => {
    try {
      return localStorage.getItem(CONFIG_SIDEBAR_KEY) === "1";
    } catch {
      return false;
    }
  });

  // The NARROW drawer is a separate piece of state from the WIDE collapse,
  // and deliberately not persisted. "I left the menu collapsed last week" is
  // a desktop preference; a drawer that covers the page is never something
  // you want restored on arrival.
  const [drawerOpen, setDrawerOpen] = useState(false);
  const shellRef = useRef<HTMLDivElement | null>(null);
  useDismissable(narrow && drawerOpen, () => setDrawerOpen(false), {
    insideRef: shellRef,
  });

  const inConfig = location.pathname.includes("/config");
  const inAdd = location.pathname.includes("/add");
  const hasSidebar = inConfig || (inAdd && !!siteSlug);

  // Opening the drawer on ARRIVAL in a sidebar area, and closing it on every
  // move inside one. Without the first, the gear lands a phone on a
  // configuration page whose navigation is off screen with no hint it
  // exists; without the second, every choice leaves the drawer covering the
  // page it just navigated to.
  const prevPath = useRef(location.pathname);
  const prevHadSidebar = useRef(hasSidebar);
  useEffect(() => {
    if (!narrow) return;
    if (hasSidebar && !prevHadSidebar.current) setDrawerOpen(true);
    else if (location.pathname !== prevPath.current) setDrawerOpen(false);
    prevPath.current = location.pathname;
    prevHadSidebar.current = hasSidebar;
  }, [location.pathname, hasSidebar, narrow]);

  const toggleSidebar = () => {
    if (narrow) {
      setDrawerOpen((v) => !v);
      return;
    }
    setConfigCollapsed((prev) => {
      const next = !prev;
      try {
        localStorage.setItem(CONFIG_SIDEBAR_KEY, next ? "1" : "0");
      } catch {
        /* ignore */
      }
      return next;
    });
  };

  const collapsed = narrow ? !drawerOpen : configCollapsed;

  const path = location.pathname;

  // ---- the navigation mode (ADR-0013) ------------------------------------
  // Switching or routing: it filters WHICH BAR is on screen and nothing else.
  // Same data, same API, same rights — authorisation is still entirely
  // server-side (ADR-0011).
  //
  // THE URL WINS OVER THE STORED PREFERENCE. The initial value is read from
  // the address before the stored one, so a deep link into /{site}/firewall
  // renders its bar on the FIRST paint instead of flashing a bar the page is
  // not in; the effect then keeps it true across in-app navigation. Without
  // this rule a link pasted into a ticket shows a page whose tab is invisible
  // to whoever opens it, which is precisely how a reading mode turns into a
  // trap.
  const [mode, setMode] = useState<NavMode>(
    () => modeForPath(location.pathname) ?? storedMode(),
  );
  const urlMode = modeForPath(path);
  useEffect(() => {
    if (urlMode && urlMode !== mode) {
      setMode(urlMode);
      // Persisted, not just applied: someone who followed a routing link and
      // worked there was last in routing, and ADR-0013 §3 promises they find
      // their mode on the way back.
      persistMode(urlMode);
    }
  }, [urlMode, mode]);

  // The ground follows the mode (ADR-73885). On <html>, like the theme,
  // because the tokens it switches are declared on :root; removed on unmount
  // so the login page keeps the switching ground whatever mode was last open.
  useEffect(() => {
    const root = document.documentElement;
    root.dataset.mode = mode;
    return () => {
      delete root.dataset.mode;
    };
  }, [mode]);

  /**
   * THE TOGGLE HAS A DESTINATION. On a screen that belongs to both modes
   * (the dashboard, every configuration and add page) we stay exactly where
   * we are and only the bar changes. On a screen the other mode owns, we go
   * to the target mode's default — because showing the routing bar over the
   * clients table would be a page with no tab, the same defect rule 2 exists
   * to prevent, arrived at from the other side.
   */
  const switchMode = (next: NavMode) => {
    if (next === mode) return;
    setMode(next);
    persistMode(next);
    const dest = destinationOnSwitch(path, next);
    if (dest && siteSlug) navigate(sitePath(siteSlug, dest));
  };

  // The screen segment: /{siteSlug}/{segment}/… — compared by EQUALITY and
  // not by `includes`, so a site whose slug happens to be "ports" does not
  // light the Ports tab on every one of its pages.
  const segment = path.split("/").filter(Boolean)[1] ?? "";
  const tabActive = (tab: string) => {
    // Add equipment hangs off the equipment tab: the gesture starts there.
    if (tab === "equipment") return inAdd || segment === "equipment";
    return segment === tab;
  };

  // The bar, from the mode's half of the application's routing table. One
  // list, used by the top bar, the phone's thumb bar AND the swipe order, so
  // the three can never disagree about what "the next page" is.
  //
  // No "Monitoring" tab: it was a second dashboard with a fixed layout, and
  // it is now one of the two dashboards every site is seeded with — reachable
  // from the Dashboard tab, beside whichever others the site has created.
  //
  // No "Configuration" tab either: configuration is reached by the GEAR
  // beside the site selector — it configures the site the selector names, so
  // the control sits against the thing it acts on. It belongs to neither
  // mode, which is why the gear never moves.
  const tabs: { key: string; label: string; to: string }[] = tabsForMode(mode).map(
    (tb) => ({
      key: tb.key,
      label: t(tb.labelKey) as string,
      to: sitePath(siteSlug, tb.segment),
    }),
  );

  // Where the gear goes. The site-less case keeps its historical target (the
  // sites screen): configuration is the ONLY reachable area before a site
  // exists, and the gear must not dead-end a fresh install.
  const configTo = siteSlug ? sitePath(siteSlug, "config", "vlans") : "/config/sites";

  const switchSite = (slug: string) => {
    if (!slug) return;
    localStorage.setItem(LAST_SITE_KEY, slug);
    const tab = tabs.find((tb) => tabActive(tb.key));
    let suffix: string[];
    if (inConfig) {
      // Config is no longer a tab (the gear owns it) but switching sites while
      // configuring must stay within configuration.
      suffix = ["config", "vlans"];
    } else if (tab?.key === "equipment" && inAdd) {
      suffix = ["add", "discover"];
    } else {
      suffix = [tab?.key ?? "dashboard"];
    }
    navigate(sitePath(slug, ...suffix));
  };

  // ---- the sidebar, as an ordered list of groups -------------------------
  const addGroups: SideGroup[] = siteSlug
    ? [
        {
          title: t("nav.addEquipment"),
          items: [
            { to: sitePath(siteSlug, "add", "discover"), label: t("nav.autoDiscovery") },
            { to: sitePath(siteSlug, "add", "ip"), label: t("nav.manualIpAdd") },
            { to: sitePath(siteSlug, "add", "serial"), label: t("nav.serialAdd") },
            { to: sitePath(siteSlug, "add", "catalog"), label: t("nav.catalogModel") },
          ],
        },
        {
          title: t("nav.tools"),
          items: [
            { to: sitePath(siteSlug, "add", "reset"), label: t("nav.resetEquipment") },
          ],
        },
        {
          title: t("nav.discoverySettings"),
          items: [
            { to: sitePath(siteSlug, "add", "prefixes"), label: t("nav.scanRanges") },
            ...(user?.is_global_admin
              ? [
                  {
                    to: "/config/credentials",
                    label: t("nav.discoveryCredentialsLink"),
                    away: true,
                  },
                ]
              : []),
          ],
        },
      ]
    : [];

  const configGroups: SideGroup[] = [
    ...(siteSlug
      ? [
          {
            title: t("nav.site"),
            items: [
              { to: sitePath(siteSlug, "config", "vlans"), label: t("nav.vlans") },
              { to: sitePath(siteSlug, "config", "management"), label: t("nav.management") },
              { to: sitePath(siteSlug, "config", "spanning-tree"), label: t("nav.spanningTree") },
              { to: sitePath(siteSlug, "config", "credentials"), label: t("nav.credentials") },
              { to: sitePath(siteSlug, "config", "tags"), label: t("nav.tags") },
              { to: sitePath(siteSlug, "config", "pipelines"), label: t("nav.pipelines") },
              { to: sitePath(siteSlug, "config", "port-profiles"), label: t("nav.portProfiles") },
              { to: sitePath(siteSlug, "config", "device-profiles"), label: t("nav.deviceProfiles") },
              { to: sitePath(siteSlug, "config", "switch-profiles"), label: t("nav.switchProfiles") },
              { to: sitePath(siteSlug, "config", "devices"), label: t("nav.equipment") },
              // What this site is backed by: a foreign controller whose
              // inventory meshcraft mirrors read-only (ADR-0015).
              { to: sitePath(siteSlug, "config", "extensions"), label: t("nav.extensions") },
              // Where this site's outages go, and whether that path has ever
              // been shown to work (ADR-0024).
              { to: sitePath(siteSlug, "config", "alerting"), label: t("nav.alerting") },
              { to: sitePath(siteSlug, "config", "import-export"), label: t("nav.importExport") },
            ],
          },
        ]
      : []),
    {
      title: t("nav.instance"),
      items: [
        { to: "/config/sites", label: t("nav.sites") },
        ...(user?.is_global_admin
          ? [{ to: "/config/users", label: t("nav.users") }]
          : []),
        ...(user?.is_global_admin
          ? [{ to: "/config/credentials", label: t("nav.discoveryCredentials") }]
          : []),
        ...(user?.is_global_admin ? [{ to: "/config/debug", label: t("nav.debug") }] : []),
        { to: "/config/proxy", label: t("nav.proxies") },
        ...(user?.is_global_admin
          ? [{ to: "/config/drivers", label: t("nav.drivers") }]
          : []),
        ...(user?.is_global_admin
          ? [{ to: "/config/agents", label: t("nav.agents") }]
          : []),
        // THE API REFERENCE IS NOT GATED. Every role has an API — a viewer's
        // token reads, an operator's applies profiles — and a document about
        // what the product refuses is exactly what the least-privileged
        // caller needs most. It sits last because it is read once, not
        // visited daily.
        { to: "/config/api", label: t("nav.api") },
      ],
    },
  ];

  const sideGroups = inAdd && siteSlug ? addGroups : configGroups;

  // ---- swipe navigation (ADR-0009) ---------------------------------------
  // Horizontal walks the five tabs; vertical walks the sidebar in menu order.
  // The recogniser is bound to the CONTENT element, so neither the sidebar
  // (which scrolls, and whose own gesture is its own) nor the bars are
  // involved.
  const contentRef = useRef<HTMLElement | null>(null);
  const swipeSide: Dest[] = sideGroups.flatMap((g) => g.items.filter((it) => !it.away));
  const pathMatches = (to: string) => path === to || path.startsWith(to + "/");
  const sideIndex = swipeSide.findIndex((it) => pathMatches(it.to));
  const tabIndex = tabs.findIndex((tb) => tabActive(tb.key));

  const lane = (axis: SwipeAxis): { list: Dest[]; index: number } => {
    if (axis === "x") {
      // No site, no tabs: they are aria-disabled in the bar too.
      return siteSlug ? { list: tabs, index: tabIndex } : { list: [], index: -1 };
    }
    return hasSidebar ? { list: swipeSide, index: sideIndex } : { list: [], index: -1 };
  };
  const target = (axis: SwipeAxis, dir: SwipeDir): Dest | null => {
    const { list, index } = lane(axis);
    const next = stepIndex(index, dir, list.length);
    return next === null ? null : list[next];
  };
  const cue = useSwipeNav({
    rootRef: contentRef,
    // A 70px accidental slide is unremarkable on a tablet and hard to make on
    // a phone held in one hand — hence the floor, and hence the breakpoint.
    floorPx: narrow ? 64 : 96,
    destination: (axis, dir) => target(axis, dir)?.label ?? null,
    go: (axis, dir) => {
      const dest = target(axis, dir);
      if (dest) navigate(dest.to);
    },
  });

  return (
    <div className="app">
      <header className="topbar">
        {/* The mark is the mode selector and ONLY that — ADR-0031. It used to
            be the way home as well, and two gestures on one object is the
            defect that ADR-0013 predicted and that use confirmed. The way
            home is the "Dashboard" tab, which is in both bars (navMode.ts)
            — here on a wide screen, in the thumb bar on a phone; and before a
            site exists the gear below leads to the only reachable area. */}
        <ModeSwitch mode={mode} onMode={switchMode} />
        <nav className="tabs">
          {tabs.map((tb) => (
            <NavLink
              key={tb.key}
              to={tb.to}
              className={tabActive(tb.key) ? "tab active" : "tab"}
              aria-disabled={!siteSlug}
              onClick={(e) => {
                if (!siteSlug) e.preventDefault();
              }}
            >
              {tb.label}
            </NavLink>
          ))}
        </nav>
        <div className="spacer" />
        <SiteSelect sites={sites ?? []} value={siteSlug} onChange={switchSite} />
        {/* The GEAR: configuration of the site the selector names — placed
            against it rather than among the observation tabs, and lit when a
            configuration screen is open so the location is never a mystery. */}
        <QuickTip text={t("nav.configuration") as string} className="quicktip-inline">
          <NavLink
            to={configTo}
            className={inConfig ? "secondary icon-btn gear-btn active" : "secondary icon-btn gear-btn"}
            aria-label={t("nav.configuration") as string}
          >
            <svg
              width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor"
              strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"
            >
              <circle cx="12" cy="12" r="3" />
              <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z" />
            </svg>
          </NavLink>
        </QuickTip>
        <AccountMenu
          displayName={user?.display_name || user?.email || ""}
          isGlobalAdmin={!!user?.is_global_admin}
          siteRole={user?.roles?.find((r) => r.site_id === sites?.find((s) => s.slug === siteSlug)?.id)?.role}
          onLogout={logout}
        />
      </header>
      <div className="main">
        {narrow && hasSidebar && drawerOpen && (
          <div
            className="sidebar-scrim"
            onClick={() => setDrawerOpen(false)}
            aria-hidden
          />
        )}
        {hasSidebar && (
          <div
            className={"sidebar-shell" + (collapsed ? " collapsed" : "")}
            ref={shellRef}
          >
            {/* ONE LINE PER ENTRY, SELECTED OR NOT. The active row is bold
                (`.sidebar a.active`), and bold text is wider: at 200px the
                selected entry reflowed onto two lines and the row height
                jumped as you moved through the menu. The label is now its own
                flex item that clips with an ellipsis instead of wrapping, and
                the rail is wide enough that no en/fr label reaches the clip.
                Icons-only with hover labels was the other option and it is
                forbidden here: a hover must never be required to READ
                content (see the `button.rename` note in styles.css). */}
            <nav className="sidebar" aria-hidden={collapsed}>
              {sideGroups.map((g) => (
                <Fragment key={g.title}>
                  <div className="section">{g.title}</div>
                  {g.items.map((it) => (
                    <NavLink key={it.to + it.label} to={it.to}>
                      <span className="nav-label">{it.label}</span>
                      {/* The glyph lives in the MARKUP, not in the string: a
                          directional character inside a translated value is
                          one an Arabic locale cannot flip, and it ended up
                          pointing the wrong way. CSS mirrors it under
                          [dir=rtl]. */}
                      {it.away && (
                        <span className="nav-away" aria-hidden>
                          ↗
                        </span>
                      )}
                    </NavLink>
                  ))}
                </Fragment>
              ))}
              {!(inAdd && siteSlug) && <VersionFooter />}
            </nav>
            <QuickTip
              text={(collapsed ? t("nav.expandMenu") : t("nav.collapseMenu")) as string}
              className="quicktip-inline"
            >
            <button
              type="button"
              className="sidebar-edge-toggle"
              onClick={toggleSidebar}
              aria-label={
                (collapsed ? t("nav.expandMenu") : t("nav.collapseMenu")) as string
              }
              aria-expanded={!collapsed}
            >
              <svg
                width="12"
                height="12"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2.5"
                strokeLinecap="round"
                strokeLinejoin="round"
                aria-hidden
              >
                {collapsed ? (
                  <path d="M9 18l6-6-6-6" />
                ) : (
                  <path d="M15 18l-6-6 6-6" />
                )}
              </svg>
            </button>
            </QuickTip>
          </div>
        )}
        <main className="content" ref={contentRef}>
          <div className="content-scroll">
            {/* A SYSTEM MESSAGE, in Configuration only — ADR-0008. It used
                to sit above every page, which was right while a skew made
                every age wrong; translating timestamps into the viewer's
                frame removed that, so this is now a fact about the
                deployment rather than a warning about the screen. */}
            {inConfig && <ClockSkewBanner />}
            <Outlet />
          </div>
        </main>
      </div>
      {/* The thumb bar. Rendered last so it comes after the page content in
          the reading and tab order, and painted above it by `position:
          fixed` — see the `.tabbar` rules. Hidden above 720px by CSS, but
          also not worth rendering there, so it is gated on the breakpoint
          the CSS uses. */}
      {narrow && (
        <nav className="tabbar" aria-label={t("nav.menu") as string}>
          {tabs.map((tb) => {
            const Icon = NAV_ICONS[tb.key];
            return (
              <NavLink
                key={tb.key}
                to={tb.to}
                className={tabActive(tb.key) ? "active" : undefined}
                aria-disabled={!siteSlug}
                onClick={(e) => {
                  if (!siteSlug) e.preventDefault();
                }}
              >
                {Icon && <Icon />}
                <span>{tb.label}</span>
              </NavLink>
            );
          })}
        </nav>
      )}
      {/* THE SWIPE CUE — ADR-0009. The page does not follow the finger; this
          pill does the only two things following it would have bought: it
          says where releasing goes, and it disappears when the finger comes
          back under the threshold, which is how the gesture is cancelled.
          `role=status` so the destination is announced rather than only
          drawn; `pointer-events: none` so it is never in the way. */}
      {cue && (
        <div className={"swipe-cue swipe-cue-" + cue.edge} role="status">
          {t("swipe.goTo", { page: cue.label })}
        </div>
      )}
    </div>
  );
}

import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
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
import LanguageSelector from "./i18n/LanguageSelector";
import Logo from "./Logo";
import { LAST_SITE_KEY, sitePath } from "./paths";
import SiteSelect from "./SiteSelect";

const CONFIG_SIDEBAR_KEY = "meshcraft.configSidebarCollapsed";

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
 * App shell: main tabs at the top
 * (Dashboard · Equipment · Topology · Monitoring · Configuration) and a
 * sidebar reserved for Configuration.
 */
export default function Layout() {
  const { t } = useTranslation();
  const { user, logout } = useAuth();
  const { siteSlug: routeSlug } = useParams();
  const navigate = useNavigate();
  const location = useLocation();

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
  const toggleConfigSidebar = () => {
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

  const inConfig = location.pathname.includes("/config");
  const inAdd = location.pathname.includes("/add");
  const path = location.pathname;
  const tabActive = (tab: string) => {
    if (tab === "config") return inConfig;
    if (tab === "equipment")
      return inAdd || /\/equipment(\/|$)/.test(path);
    if (tab === "topology") return path.includes("/topology");
    return path.includes("/" + tab);
  };

  const tabs: { key: string; label: string; to: string }[] = [
    { key: "dashboard", label: t("nav.dashboard"), to: sitePath(siteSlug, "dashboard") },
    { key: "equipment", label: t("nav.equipment"), to: sitePath(siteSlug, "equipment") },
    { key: "ports", label: t("nav.ports"), to: sitePath(siteSlug, "ports") },
    { key: "topology", label: t("nav.topology"), to: sitePath(siteSlug, "topology") },
    { key: "monitoring", label: t("nav.monitoring"), to: sitePath(siteSlug, "monitoring") },
    {
      key: "config",
      label: t("nav.configuration"),
      to: siteSlug ? sitePath(siteSlug, "config", "vlans") : "/config/sites",
    },
  ];

  const switchSite = (slug: string) => {
    if (!slug) return;
    localStorage.setItem(LAST_SITE_KEY, slug);
    const tab = tabs.find((tb) => tabActive(tb.key));
    let suffix: string[];
    if (tab?.key === "config") {
      suffix = ["config", "vlans"];
    } else if (tab?.key === "equipment" && inAdd) {
      suffix = ["add", "discover"];
    } else {
      suffix = [tab?.key ?? "dashboard"];
    }
    navigate(sitePath(slug, ...suffix));
  };

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">
          <Logo />
        </div>
        <nav className="tabs">
          {tabs.map((tb) => (
            <NavLink
              key={tb.key}
              to={tb.to}
              className={tabActive(tb.key) ? "tab active" : "tab"}
              aria-disabled={!siteSlug && tb.key !== "config"}
              onClick={(e) => {
                if (!siteSlug && tb.key !== "config") e.preventDefault();
              }}
            >
              {tb.label}
            </NavLink>
          ))}
        </nav>
        <div className="spacer" />
        <SiteSelect sites={sites ?? []} value={siteSlug} onChange={switchSite} />
        <span className="note">
          {user?.display_name || user?.email}
          {user?.is_global_admin && (
            <span className="badge admin" style={{ marginLeft: 8 }}>
              {t("common.globalAdmin")}
            </span>
          )}
        </span>
        <LanguageSelector />
        <QuickTip text={t("common.logout") as string} className="quicktip-inline">
          <button
            className="secondary icon-btn"
            onClick={logout}
            aria-label={t("common.logout") as string}
          >
            <svg
              width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor"
              strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"
            >
              <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
              <path d="M16 17l5-5-5-5" />
              <path d="M21 12H9" />
            </svg>
          </button>
        </QuickTip>
      </header>
      <div className="main">
        {inAdd && siteSlug && (
          <nav className="sidebar">
            <div className="section">{t("nav.addEquipment")}</div>
            <NavLink to={sitePath(siteSlug, "add", "discover")}>{t("nav.autoDiscovery")}</NavLink>
            <NavLink to={sitePath(siteSlug, "add", "ip")}>{t("nav.manualIpAdd")}</NavLink>
            <NavLink to={sitePath(siteSlug, "add", "serial")}>{t("nav.serialAdd")}</NavLink>
            <NavLink to={sitePath(siteSlug, "add", "catalog")}>{t("nav.catalogModel")}</NavLink>
            <div className="section">{t("nav.tools")}</div>
            <NavLink to={sitePath(siteSlug, "add", "reset")}>{t("nav.resetEquipment")}</NavLink>
            <div className="section">{t("nav.discoverySettings")}</div>
            <NavLink to={sitePath(siteSlug, "add", "prefixes")}>{t("nav.scanRanges")}</NavLink>
            {user?.is_global_admin && (
              <NavLink to="/config/credentials">{t("nav.discoveryCredentialsLink")}</NavLink>
            )}
          </nav>
        )}
        {inConfig && (
          <div className={"sidebar-shell" + (configCollapsed ? " collapsed" : "")}>
            <nav className="sidebar" aria-hidden={configCollapsed}>
              {siteSlug && (
                <>
                  <div className="section">{t("nav.site")}</div>
                  <NavLink to={sitePath(siteSlug, "config", "vlans")}>{t("nav.vlans")}</NavLink>
                  <NavLink to={sitePath(siteSlug, "config", "management")}>{t("nav.management")}</NavLink>
                  <NavLink to={sitePath(siteSlug, "config", "credentials")}>{t("nav.credentials")}</NavLink>
                  <NavLink to={sitePath(siteSlug, "config", "tags")}>{t("nav.tags")}</NavLink>
                  <NavLink to={sitePath(siteSlug, "config", "pipelines")}>{t("nav.pipelines")}</NavLink>
                  <NavLink to={sitePath(siteSlug, "config", "port-profiles")}>{t("nav.portProfiles")}</NavLink>
                  <NavLink to={sitePath(siteSlug, "config", "device-profiles")}>
                    {t("nav.deviceProfiles")}
                  </NavLink>
                  <NavLink to={sitePath(siteSlug, "config", "switch-profiles")}>
                    {t("nav.switchProfiles")}
                  </NavLink>
                  <NavLink to={sitePath(siteSlug, "config", "devices")}>{t("nav.equipment")}</NavLink>
                  <NavLink to={sitePath(siteSlug, "config", "import-export")}>{t("nav.importExport")}</NavLink>
                </>
              )}
              <div className="section">{t("nav.instance")}</div>
              <NavLink to="/config/sites">{t("nav.sites")}</NavLink>
              {user?.is_global_admin && <NavLink to="/config/users">{t("nav.users")}</NavLink>}
              {user?.is_global_admin && (
                <NavLink to="/config/credentials">{t("nav.discoveryCredentials")}</NavLink>
              )}
              {user?.is_global_admin && <NavLink to="/config/debug">{t("nav.debug")}</NavLink>}
              <NavLink to="/config/proxy">{t("nav.proxies")}</NavLink>
              <VersionFooter />
            </nav>
            <QuickTip
              text={(configCollapsed ? t("nav.expandMenu") : t("nav.collapseMenu")) as string}
              className="quicktip-inline"
            >
            <button
              type="button"
              className="sidebar-edge-toggle"
              onClick={toggleConfigSidebar}
              aria-label={
                (configCollapsed ? t("nav.expandMenu") : t("nav.collapseMenu")) as string
              }
              aria-expanded={!configCollapsed}
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
                {configCollapsed ? (
                  <path d="M9 18l6-6-6-6" />
                ) : (
                  <path d="M15 18l-6-6 6-6" />
                )}
              </svg>
            </button>
            </QuickTip>
          </div>
        )}
        <main className="content">
          <div className="content-scroll">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  );
}
